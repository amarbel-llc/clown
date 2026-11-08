package pluginhost

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Stop marks the server as stopping, then tells a recording bridge the SIGTERM
// is on purpose, then sends the SIGTERM: a stop on purpose must not be reported
// by WatchExits, and the bridge must hear the notice while it is still alive.
func TestStopMarksStoppingThenNotifiesThenSignals(t *testing.T) {
	var stoppingAtNotice, aliveAfterHold atomic.Bool
	var notices atomic.Int32
	var s *ManagedServer
	var pid int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clown/shutdown" {
			http.NotFound(w, r)
			return
		}
		notices.Add(1)
		stoppingAtNotice.Store(s.stopping.Load())
		// Hold the notice. If the SIGTERM had been sent first, the child would
		// be gone (and reaped) by the time we look.
		time.Sleep(300 * time.Millisecond)
		aliveAfterHold.Store(syscall.Kill(pid, 0) == nil)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid = cmd.Process.Pid
	stderrDone := make(chan struct{})
	close(stderrDone)
	s = &ManagedServer{
		Name:       "p/s",
		PluginName: "p",
		Def:        ServerDef{Env: map[string]string{EnvBridgeRecordDir: "/r"}},
		cmd:        cmd,
		handshake:  Handshake{NetworkType: "tcp", Address: srv.Listener.Addr().(*net.TCPAddr).String()},
		done:       make(chan struct{}),
		stderrDone: stderrDone,
		startedAt:  time.Now(),
	}
	go s.reap()

	s.Stop()

	select {
	case <-s.done:
	default:
		t.Fatal("Stop returned before the process was reaped")
	}
	if notices.Load() != 1 {
		t.Fatalf("the bridge got %d shutdown notices, want 1", notices.Load())
	}
	if !stoppingAtNotice.Load() {
		t.Error("the notice went out before the server was marked stopping")
	}
	if !aliveAfterHold.Load() {
		t.Error("the process was already gone while the notice was held: the SIGTERM did not wait for the notice")
	}
}

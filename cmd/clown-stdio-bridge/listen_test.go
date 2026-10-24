package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F12 (FDR 0020): a confined session's serve bridge listens on a per-session
// unix socket, peer-checked against its unit, instead of loopback TCP.

func TestParseArgsListenUnix(t *testing.T) {
	got, err := parseArgs([]string{"--listen-unix", "/run/x/spinclass.sock", "--peer-unit", "u-1.service", "--command", "spinclass", "--", "serve"})
	if err != nil {
		t.Fatal(err)
	}
	if got.listenUnix != "/run/x/spinclass.sock" || got.peerUnit != "u-1.service" || got.command != "spinclass" || !slicesEqual(got.args, []string{"serve"}) {
		t.Fatalf("parsed = %#v", got)
	}
	for name, in := range map[string][]string{
		"socket without unit": {"--listen-unix", "/run/x/s.sock", "--command", "x"},
		"unit without socket": {"--peer-unit", "u-1.service", "--command", "x"},
		"relative socket":     {"--listen-unix", "s.sock", "--peer-unit", "u-1.service", "--command", "x"},
	} {
		if _, err := parseArgs(in); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestHandshakeLineNamesTheListener(t *testing.T) {
	if got := handshakeLine("tcp", "127.0.0.1:4242"); got != "1|1|tcp|127.0.0.1:4242|streamable-http" {
		t.Errorf("tcp handshake = %q", got)
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	if got := handshakeLine("unix", sock); got != "1|1|unix|"+sock+"|streamable-http" {
		t.Errorf("unix handshake = %q", got)
	}
}

// A serve bridge admits its parent clown by pid, so it must not outlive it:
// once the parent is gone the bridge stops (F12 review).
func TestWatchParentReportsAParentChange(t *testing.T) {
	ppids := []int{42, 42, 1}
	i := 0
	getppid := func() int {
		p := ppids[min(i, len(ppids)-1)]
		i++
		return p
	}
	gone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchParent(ctx, 42, getppid, time.Millisecond, func() { close(gone) })
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("a changed parent pid was not reported")
	}
}

// listen picks the unix listener only when asked; without --listen-unix the
// bridge is unchanged (loopback TCP).
func TestListenDefaultsToLoopbackTCP(t *testing.T) {
	ln, network, err := listen(parsedArgs{command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if network != "tcp" || !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Errorf("listen = %s %s, want loopback tcp", network, ln.Addr())
	}
	sock := filepath.Join(t.TempDir(), "serve", "p1", "spinclass.sock")
	uln, network, err := listen(parsedArgs{command: "x", listenUnix: sock, peerUnit: "u-1.service"})
	if err != nil {
		t.Fatal(err)
	}
	defer uln.Close()
	if network != "unix" || uln.Addr().String() != sock {
		t.Errorf("listen = %s %s, want unix %s", network, uln.Addr(), sock)
	}
}

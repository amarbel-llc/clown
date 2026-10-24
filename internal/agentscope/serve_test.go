package agentscope

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// F12 (FDR 0020): the per-session serve socket.

const otherUnitCgroup = "/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/u-2.service"

// serveOnce listens like the bridge does and reports, for one caller whose
// cgroup reads as callerCgroup, whether its connection was accepted.
func serveOnce(t *testing.T, path, unit string, frontendPID int, callerCgroup string) bool {
	t.Helper()
	ln, err := ListenServeSocket(path, unit, frontendPID, func(int) (string, error) { return callerCgroup, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A refused caller is closed by the listener and reads EOF; an accepted
	// one is handed to Accept. Whichever happens first decides, so no timing.
	refused := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 1))
		refused <- err
	}()
	select {
	case s := <-accepted:
		s.Close()
		return true
	case err := <-refused:
		if err != io.EOF {
			t.Errorf("refused caller read %v, want EOF", err)
		}
		return false
	case <-time.After(10 * time.Second):
		t.Fatal("neither accepted nor refused")
		return false
	}
}

func TestServeSocketAcceptsOnlyItsUnitAndTheFrontend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serve", "p1", "spinclass.sock")
	if !serveOnce(t, path, "u-1.service", 0, unitCgroup) {
		t.Error("a caller in the session's own unit was refused")
	}
	if serveOnce(t, path, "u-1.service", 0, "/user.slice/user-1001.slice/session-3.scope") {
		t.Error("a host process outside any unit was accepted")
	}
	if serveOnce(t, path, "u-1.service", 0, otherUnitCgroup) {
		t.Error("another session's unit was accepted")
	}
	// The frontend (the bridge's parent) checks health and fetches the
	// prompt fragment; it is accepted by pid whatever its cgroup.
	if !serveOnce(t, path, "u-1.service", os.Getpid(), "/user.slice/user-1001.slice/session-3.scope") {
		t.Error("the frontend's own pid was refused")
	}
}

func TestServeSocketModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve", "p1", "spinclass.sock")
	ln, err := ListenServeSocket(path, "u-1.service", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s mode %o, want %o", p, got, want)
		}
	}
}

// Two sessions get two sockets, each its own bridge's: a caller in one
// session's unit is accepted by that session's socket only, so it never
// reaches the other session's child.
func TestTwoSessionsNeverShareAServeSocket(t *testing.T) {
	runtime := t.TempDir()
	a, err := ServeSocketPath(runtime, "principal-a", "spinclass")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ServeSocketPath(runtime, "principal-b", "spinclass")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || filepath.Dir(a) == filepath.Dir(b) {
		t.Fatalf("sessions share a socket or its directory: %s, %s", a, b)
	}
	if !serveOnce(t, a, "u-1.service", 0, unitCgroup) || serveOnce(t, b, "u-2.service", 0, unitCgroup) {
		t.Error("session a's unit must reach a's socket and be refused by b's")
	}
}

func TestServeSocketPathRefusesBadPrincipalsAndLongPaths(t *testing.T) {
	for _, p := range []string{"", "..", "a/b", "."} {
		if _, err := ServeSocketPath("/run/user/1001", p, "spinclass"); err == nil {
			t.Errorf("principal %q: want refusal", p)
		}
	}
	if _, err := ServeSocketPath("/run/user/1001", strings.Repeat("x", 100), "spinclass"); err == nil {
		t.Error("want refusal of a path over the unix socket limit")
	}
	got, err := ServeSocketPath("/run/user/1001", "p1", "spinclass")
	if err != nil || got != "/run/user/1001/clown/serve/p1/spinclass.sock" {
		t.Errorf("ServeSocketPath = %q, %v", got, err)
	}
}

// The serve socket is bound into the unit and relayed from the port claude's
// URL names; the result reports it.
func TestLaunchBindsRelaysAndReportsTheServeSocket(t *testing.T) {
	l := testLaunch()
	l.Serve = Relay{Listen: "127.0.0.1:19000", Socket: "/run/user/1001/clown/serve/p1/spinclass.sock"}
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--property=BindPaths=" + l.Serve.Socket, "--relay", "127.0.0.1:19000=" + l.Serve.Socket} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	if r := l.baseResult(); r.ServeSocket != l.Serve.Socket {
		t.Errorf("result serve_socket = %q, want %q", r.ServeSocket, l.Serve.Socket)
	}
	if !slices.Contains(Capabilities, "frontend-serve-socket") {
		t.Error("capability frontend-serve-socket is not advertised")
	}
	none := testLaunch()
	if r := none.baseResult(); r.ServeSocket != "" {
		t.Errorf("a launch without a serve socket reports %q", r.ServeSocket)
	}
}

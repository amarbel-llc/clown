package agentscope

import (
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// readsEOF reports whether c reaches EOF (or another read error) within d.
func readsEOF(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 64)
	for {
		_, err := c.Read(buf)
		if err == nil {
			continue
		}
		var ne net.Error
		return !(errors.As(err, &ne) && ne.Timeout())
	}
}

// The frontend holds the first connection from the unit and accepts no
// other: the agent cannot open a second one to keep anything alive, and
// what is written on the held one changes nothing. Closing the frontend's
// end is the only signal (clown#319).
func TestDeadManHoldsOnlyTheFirstUnitConnection(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "deadman.sock")
	d := &DeadMan{Unit: "u-1.service", PeerCgroup: func(int) (string, error) { return unitCgroup, nil }}
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	held, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	// A second connection is refused once the first is held.
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.Dial("unix", sock)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("a second connection is still accepted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Keepalive-style bytes from the unit side do nothing.
	if _, err := held.Write([]byte("keepalive\n")); err != nil {
		t.Fatal(err)
	}
	if readsEOF(held, 300*time.Millisecond) {
		t.Fatal("the held connection ended while the frontend lives")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if !readsEOF(held, 2*time.Second) {
		t.Fatal("closing the frontend's end did not reach the unit as EOF")
	}
}

// The held connection stays referenced while the frontend lives: a garbage
// collection does not close it (an unreferenced *net.UnixConn would be
// finalized and the shim would see EOF).
func TestDeadManHeldConnSurvivesGC(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "deadman.sock")
	d := &DeadMan{Unit: "u-1.service", PeerCgroup: func(int) (string, error) { return unitCgroup, nil }}
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	held, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	time.Sleep(100 * time.Millisecond)
	for range 3 {
		runtime.GC()
	}
	if readsEOF(held, 500*time.Millisecond) {
		t.Fatal("the held connection ended after a GC while the frontend lives")
	}
}

// A Close that races the first accept still closes the accepted connection.
func TestDeadManCloseBeforeAcceptClosesTheLateConnection(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "deadman.sock")
	release := make(chan struct{})
	d := &DeadMan{Unit: "u-1.service", PeerCgroup: func(int) (string, error) {
		<-release // hold the accept between Accept and storing the conn
		return unitCgroup, nil
	}}
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(100 * time.Millisecond)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if !readsEOF(c, 2*time.Second) {
		t.Fatal("a connection accepted after Close was held open")
	}
}

// The capability is advertised, so spinclass can require it.
func TestDeadManCapabilityIsAdvertised(t *testing.T) {
	if !slices.Contains(Capabilities, "frontend-deadman") {
		t.Error("capability frontend-deadman is not advertised")
	}
}

// A caller outside the unit is refused and does not use up the slot.
func TestDeadManRefusesAPeerOutsideTheUnit(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "deadman.sock")
	var calls atomic.Int32
	d := &DeadMan{Unit: "u-1.service", PeerCgroup: func(int) (string, error) {
		if calls.Add(1) == 1 {
			return "/user.slice/other.service", nil
		}
		return unitCgroup, nil
	}}
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	outsider, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer outsider.Close()
	if !readsEOF(outsider, 2*time.Second) {
		t.Fatal("a caller outside the unit was held")
	}
	shim, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("the unit's connection after a refused outsider: %v", err)
	}
	defer shim.Close()
	if readsEOF(shim, 300*time.Millisecond) {
		t.Fatal("the unit's connection was not held")
	}
}

// WatchFrontend is the shim's end: its channel closes on EOF, never on
// content.
func TestWatchFrontendClosesOnEOFOnly(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "fe.sock")
	ln, err := net.Listen("unix", sock)
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
	gone, err := WatchFrontend(sock)
	if err != nil {
		t.Fatal(err)
	}
	var fe net.Conn
	select {
	case fe = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("WatchFrontend did not connect")
	}
	if _, err := fe.Write([]byte("anything")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gone:
		t.Fatal("content was read as frontend death")
	case <-time.After(300 * time.Millisecond):
	}
	fe.Close()
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("EOF did not signal frontend death")
	}
	if _, err := WatchFrontend(filepath.Join(t.TempDir(), "absent.sock")); err == nil {
		t.Fatal("no frontend to connect to: want an error (the shim refuses)")
	}
}

// The unit binds the dead-man socket and the shim is told to watch it.
func TestSystemdRunArgsCarryTheDeadMan(t *testing.T) {
	l := testLaunch()
	l.DeadManSocket = "/run/user/1001/clown/agent/u-1/deadman.sock"
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--property=BindPaths="+l.DeadManSocket) {
		t.Errorf("no bind of the dead-man socket in %v", args)
	}
	sep := slices.Index(args, "--")
	tail := args[sep+1:]
	i := slices.Index(tail, "--deadman")
	if i < 0 || i+1 >= len(tail) || tail[i+1] != l.DeadManSocket || i > slices.Index(tail, "--") {
		t.Errorf("shim args %v: want --deadman %s before the provider", tail, l.DeadManSocket)
	}
	l.DeadManSocket = ""
	args, _ = l.SystemdRunArgs()
	if slices.Contains(args, "--deadman") {
		t.Errorf("--deadman without a socket: %v", args)
	}
}

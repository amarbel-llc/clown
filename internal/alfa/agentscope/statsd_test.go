package agentscope

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// statsdSink is the frontend's statsd stand-in: a UDP listener on loopback.
func statsdSink(t *testing.T) (addr string, next func() string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	return pc.LocalAddr().String(), func() string {
		buf := make([]byte, 2048)
		_ = pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return ""
		}
		return string(buf[:n])
	}
}

func statsdRelayFor(t *testing.T, target string, cgroup func(int) (string, error)) string {
	t.Helper()
	r := &StatsdRelay{Target: target, Unit: "u-1.service", PeerCgroup: cgroup}
	sock := filepath.Join(t.TempDir(), "statsd.sock")
	if err := r.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return sock
}

func sendDatagram(t *testing.T, sock, payload string) {
	t.Helper()
	c, err := net.Dial("unixgram", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
}

func TestStatsdRelaySendsTheUnitsDatagramsUnchanged(t *testing.T) {
	target, next := statsdSink(t)
	// Written by the relay's serve goroutine, read here: the datagram's trip
	// through the kernel to the sink is no happens-before edge for the race
	// detector (clown#306), so the pid crosses goroutines atomically.
	var sawPid atomic.Int64
	sock := statsdRelayFor(t, target, func(pid int) (string, error) {
		sawPid.Store(int64(pid))
		return unitCgroup, nil
	})
	payload := "clown.toolcost.calls.Read:1|c\nclown.toolcost.bytes.Read:42|ms"
	sendDatagram(t, sock, payload)
	if got := next(); got != payload {
		t.Fatalf("statsd got %q, want %q", got, payload)
	}
	// The cgroup check ran on the kernel-attached sender pid, not on
	// anything the sender wrote.
	if got := sawPid.Load(); got != int64(os.Getpid()) {
		t.Fatalf("cgroup looked up for pid %d, want the sender's %d", got, os.Getpid())
	}
}

func TestStatsdRelayDropsDatagramsFromOutsideTheUnit(t *testing.T) {
	for _, cg := range []string{
		"/user.slice/user-1001.slice/session-3.scope",
		"/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/other.service",
	} {
		target, next := statsdSink(t)
		sock := statsdRelayFor(t, target, func(int) (string, error) { return cg, nil })
		sendDatagram(t, sock, "x:1|c")
		if got := next(); got != "" {
			t.Errorf("caller in %q reached statsd with %q", cg, got)
		}
	}
}

func TestStatsdRelaySocketIsPrivate(t *testing.T) {
	target, _ := statsdSink(t)
	sock := statsdRelayFor(t, target, func(int) (string, error) { return unitCgroup, nil })
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v, want 0600", fi.Mode().Perm())
	}
}

// TestUDPRelayCarriesDatagramsEndToEnd runs both ends: the in-unit UDP
// listener RunRelays starts, and the frontend StatsdRelay behind it.
func TestUDPRelayCarriesDatagramsEndToEnd(t *testing.T) {
	target, next := statsdSink(t)
	sock := statsdRelayFor(t, target, func(int) (string, error) { return unitCgroup, nil })

	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.LocalAddr().String()
	probe.Close()

	ready := make(chan struct{})
	go func() { _ = RunRelays([]Relay{{Listen: listen, Socket: sock, UDP: true}}, func() { close(ready) }) }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("relay never became ready")
	}
	c, err := net.Dial("udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, payload := range []string{"a:1|c", "b:2|g"} {
		if _, err := c.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		if got := next(); got != payload {
			t.Fatalf("statsd got %q, want %q", got, payload)
		}
	}
}

func TestParseRelayUDP(t *testing.T) {
	r, err := ParseRelay("udp:127.0.0.1:8125=/run/x/statsd.sock")
	if err != nil || !r.UDP || r.Listen != "127.0.0.1:8125" || r.String() != "udp:127.0.0.1:8125=/run/x/statsd.sock" {
		t.Fatalf("ParseRelay = %+v, %v", r, err)
	}
	if _, err := ParseRelay("udp:=/run/x/statsd.sock"); err == nil {
		t.Fatal("want error for a udp relay without an address")
	}
}

func TestSystemdRunArgsBindAndRelayTheStatsdSocket(t *testing.T) {
	l := testLaunch()
	l.StatsdSocket = "/run/user/1001/clown/agent/u-1/statsd.sock"
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--property=BindPaths=/run/user/1001/clown/agent/u-1/statsd.sock",
		"udp:127.0.0.1:8125=/run/user/1001/clown/agent/u-1/statsd.sock",
		"--setenv=STATSD_HOST=127.0.0.1",
		"--setenv=STATSD_PORT=8125",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
}

func TestNoStatsdSocketMeansNoStatsdRelayOrEnv(t *testing.T) {
	args, err := testLaunch().SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.Contains(a, "udp:") || strings.Contains(a, "STATSD_") {
			t.Errorf("unexpected statsd wiring %q", a)
		}
	}
}

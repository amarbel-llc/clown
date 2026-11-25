package peercheck

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckCgroupExact(t *testing.T) {
	const u = "/user.slice/user-1000.slice/user@1000.service/clown.slice/clown-agent.slice/clown-agent-x.service"
	for name, c := range map[string]struct {
		cg, want string
		ok       bool
	}{
		"equal": {u, u, true},
		// Strict equality (round 1, M2): a sub-cgroup is NOT the unit.
		"sub-cgroup":        {u + "/app.scope", u, false},
		"look-alike suffix": {"/user.slice/evil/clown.slice/clown-agent.slice/clown-agent-x.service", u, false},
		"sibling prefix":    {u + "-2", u, false},
		"parent":            {"/user.slice/user-1000.slice/user@1000.service/clown.slice/clown-agent.slice", u, false},
		"empty want":        {u, "", false},
		"relative want":     {u, "user.slice", false},
		"trailing slash":    {u, u + "/", false},
		"nothing in it":     {"", u, false},
	} {
		if err := CheckCgroupExact(c.cg, c.want); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
	}
}

func TestParsePidfdInfo(t *testing.T) {
	if pid, err := parsePidfdInfo([]byte("pos:\t0\nflags:\t02000002\nPid:\t1234\nNSpid:\t1234\t5\n")); err != nil || pid != 1234 {
		t.Errorf("pid %d, %v", pid, err)
	}
	for _, in := range []string{"Pid:\t-1\n", "Pid:\t0\n", "Pid:\tx\n", "flags:\t0\n", ""} {
		if _, err := parsePidfdInfo([]byte(in)); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

// The real thing against a peer we know: ourselves, over a real unix socket.
func TestCheckPeerExactAgainstASelfConnection(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pce")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cl, err := net.Dial("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	me, err := ProcCgroup(os.Getpid())
	if err != nil {
		t.Skip("no cgroup v2 entry:", err)
	}
	if err := CheckPeerExact(srv, me); err != nil {
		// Only the specific "this kernel has no SO_PEERPIDFD" answer skips
		// (round 1, M4); every other failure is a failure.
		if errors.Is(err, unix.ENOPROTOOPT) {
			t.Skip("kernel without SO_PEERPIDFD:", err)
		}
		t.Fatalf("own cgroup refused: %v", err)
	}
	if err := CheckPeerExact(srv, me+"/nope"); err == nil {
		t.Error("a deeper cgroup than ours was accepted")
	}
	if err := CheckPeerExact(srv, "/somewhere/else"); err == nil {
		t.Error("another cgroup was accepted")
	}
}

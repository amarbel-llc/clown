package peercheck

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const opsUnit = "/user.slice/user-1000.slice/user@1000.service/clown.slice/clown-agent.slice/clown-agent-x.service"

// oneConn is the accepted end of a real unix connection (checkPeerExactWith
// needs a *net.UnixConn; the fake ops never use its credentials).
func oneConn(t *testing.T) net.Conn {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cpo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	acc := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		acc <- c
	}()
	cl, err := net.Dial("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	c := <-acc
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// fakeOps records what was called and in what order.
type fakeOps struct {
	calls       []string
	pidfdErr    error
	pidErr      error
	cgroup      string
	cgroupErr   error
	aliveErr    error
	closed      int
	cgroupOfPid int
}

func (f *fakeOps) ops() peerOps {
	return peerOps{
		pidfdOf: func(*net.UnixConn) (int, error) {
			f.calls = append(f.calls, "pidfd")
			if f.pidfdErr != nil {
				return -1, f.pidfdErr
			}
			return 7, nil
		},
		pidOf: func(fd int) (int, error) {
			f.calls = append(f.calls, "pid")
			if f.pidErr != nil {
				return 0, f.pidErr
			}
			return 4242, nil
		},
		cgroupOf: func(pid int) (string, error) {
			f.calls = append(f.calls, "cgroup")
			f.cgroupOfPid = pid
			return f.cgroup, f.cgroupErr
		},
		alive: func(int) error {
			f.calls = append(f.calls, "alive")
			return f.aliveErr
		},
		close: func(int) { f.closed++ },
	}
}

// A peer in the unit's cgroup is admitted, the cgroup read is of the pid the
// pidfd named, liveness is confirmed AFTER that read, and the pidfd is closed.
// Red if the liveness line moves before the read or is removed (the pid-reuse
// defence), or if the read uses any other pid.
func TestCheckPeerExactOrderAndPid(t *testing.T) {
	f := &fakeOps{cgroup: opsUnit + "/app.scope"}
	if err := checkPeerExactWith(oneConn(t), opsUnit, f.ops()); err != nil {
		t.Fatalf("a peer in the unit was refused: %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "pidfd,pid,cgroup,alive" {
		t.Errorf("order = %s, want pidfd,pid,cgroup,alive (liveness after the cgroup read)", got)
	}
	if f.cgroupOfPid != 4242 {
		t.Errorf("read the cgroup of pid %d, want the pidfd's 4242", f.cgroupOfPid)
	}
	if f.closed != 1 {
		t.Errorf("the pidfd was closed %d times, want once", f.closed)
	}
}

// Refusals: no pidfd (never a fallback), a pidfd naming nobody visible, an
// unreadable cgroup, a peer gone by the time of the re-check, and a peer in
// another cgroup. Each refuses, and the pidfd is closed when there was one.
func TestCheckPeerExactRefusesEachFailure(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]struct {
		f          fakeOps
		wantClosed int
		wantIn     string
	}{
		"no pidfd (old kernel)":  {fakeOps{pidfdErr: boom}, 0, "failing closed"},
		"pid not visible":        {fakeOps{pidErr: boom}, 1, "boom"},
		"cgroup unreadable":      {fakeOps{cgroupErr: boom}, 1, "boom"},
		"peer gone (pid reused)": {fakeOps{cgroup: opsUnit, aliveErr: boom}, 1, "is gone"},
		"another cgroup":         {fakeOps{cgroup: "/user.slice/user-1000.slice/session-3.scope"}, 1, "not in"},
		"look-alike path":        {fakeOps{cgroup: "/x/clown-agent.slice/clown-agent-x.service"}, 1, "not in"},
	}
	for name, c := range cases {
		f := c.f
		err := checkPeerExactWith(oneConn(t), opsUnit, f.ops())
		if err == nil || !strings.Contains(err.Error(), c.wantIn) {
			t.Errorf("%s: err = %v, want a refusal containing %q", name, err, c.wantIn)
		}
		if f.closed != c.wantClosed {
			t.Errorf("%s: pidfd closed %d times, want %d", name, f.closed, c.wantClosed)
		}
	}
	// A non-unix connection is refused outright.
	if err := checkPeerExactWith(nopConn{}, opsUnit, (&fakeOps{}).ops()); err == nil {
		t.Error("a non-unix connection was admitted")
	}
}

type nopConn struct{ net.Conn }

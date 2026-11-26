package peercheck

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

const helperSockEnv = "CLOWN_PEERCHECK_HELPER_SOCK"

// TestPeerHelperProcess is not a test: it is the peer, run as a child of the
// tests below (the usual re-exec pattern). It connects to the socket named in
// the environment and then blocks until it is killed.
func TestPeerHelperProcess(t *testing.T) {
	sock := os.Getenv(helperSockEnv)
	if sock == "" {
		return
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		os.Exit(3)
	}
	defer c.Close()
	buf := make([]byte, 1)
	_, _ = c.Read(buf) // blocks until the parent kills this process
	os.Exit(0)
}

// startPeer starts the helper as a real child process, accepts its connection
// and returns the accepted end, the child, and a function that kills and reaps
// it.
func startPeer(t *testing.T) (net.Conn, *exec.Cmd, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cpc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestPeerHelperProcess$")
	cmd.Env = append(os.Environ(), helperSockEnv+"="+sock)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	killAndReap := func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			reaped = true
		}
	}
	t.Cleanup(killAndReap)
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, cmd, killAndReap
}

// Round 1, M3: the production wiring (realPeerOps), not the fakes, against a
// peer that is a DIFFERENT process. It names the child's pid (not the test's
// own), admits the child while it lives, and after the child is killed and
// reaped the live check fails and the connection is refused.
// Red if pidOf read our own pid, or alive always returned nil.
func TestRealOpsAgainstAChildThatIsKilledAndReaped(t *testing.T) {
	srv, cmd, killAndReap := startPeer(t)
	me, err := ProcCgroup(os.Getpid())
	if errors.Is(err, ErrNoCgroupV2) {
		t.Skip("no cgroup v2 entry:", err) // the one skippable answer
	} else if err != nil {
		t.Fatalf("reading our own cgroup: %v", err)
	}
	uc := srv.(*net.UnixConn)
	pidfd, err := realPeerOps.pidfdOf(uc)
	if errors.Is(err, unix.ENOPROTOOPT) {
		t.Skip("kernel without SO_PEERPIDFD:", err)
	}
	if err != nil {
		t.Fatalf("pidfd: %v", err)
	}
	defer realPeerOps.close(pidfd)

	pid, err := realPeerOps.pidOf(pidfd)
	if err != nil {
		t.Fatal(err)
	}
	if pid != cmd.Process.Pid || pid == os.Getpid() {
		t.Fatalf("the pidfd names pid %d; the child is %d and this process is %d", pid, cmd.Process.Pid, os.Getpid())
	}
	if err := realPeerOps.alive(pidfd); err != nil {
		t.Fatalf("a live child reported gone: %v", err)
	}
	// The child shares this process's cgroup in a test, so it is admitted by it.
	if err := CheckPeerExact(srv, me); err != nil {
		t.Fatalf("a live child in our cgroup was refused: %v", err)
	}

	killAndReap()
	if err := realPeerOps.alive(pidfd); err == nil {
		t.Error("a killed and reaped child still reported alive (pid reuse would be undetected)")
	}
	if err := CheckPeerExact(srv, me); err == nil {
		t.Error("a killed and reaped peer was admitted")
	}
}

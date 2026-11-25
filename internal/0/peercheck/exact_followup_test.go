package peercheck

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeProc writes <root>/<pid>/cgroup with the given content.
func writeProc(t *testing.T, root string, pid int, content string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ProcCgroupAt: the v2 line is found in a pure v2 and a hybrid file; a v1-only
// file is ErrNoCgroupV2 (the one answer the real-kernel tests may skip on); a
// missing file is NOT that (a vanished pid must fail, not skip).
func TestProcCgroupAtAnswers(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, 10, "0::/a/b.service\n")
	writeProc(t, root, 11, "12:cpu,cpuacct:/old\n1:name=systemd:/old\n0::/hybrid/c.service\n")
	writeProc(t, root, 12, "12:cpu,cpuacct:/old\n1:name=systemd:/old\n")
	for pid, want := range map[int]string{10: "/a/b.service", 11: "/hybrid/c.service"} {
		if got, err := ProcCgroupAt(root, pid); err != nil || got != want {
			t.Errorf("pid %d: %q, %v, want %q", pid, got, err, want)
		}
	}
	if _, err := ProcCgroupAt(root, 12); !errors.Is(err, ErrNoCgroupV2) {
		t.Errorf("v1-only: err = %v, want ErrNoCgroupV2", err)
	}
	if _, err := ProcCgroupAt(root, 13); err == nil || errors.Is(err, ErrNoCgroupV2) {
		t.Errorf("a missing pid: err = %v, want a plain failure (a test must not skip on it)", err)
	}
}

// The check reads the PEER's cgroup, not the reader's: with a fake proc tree
// in which the reader (this process) is in one cgroup and the peer in the
// unit's, want = the unit admits, want = the reader's own cgroup refuses. A
// real-ops child test cannot tell the two apart (a child inherits our cgroup);
// this one can. Red if cgroupOf were given os.Getpid().
func TestTheCgroupReadIsThePeersNotTheReaders(t *testing.T) {
	root := t.TempDir()
	const peerPID = 4242
	reader := "/user.slice/user-1000.slice/session-3.scope"
	writeProc(t, root, os.Getpid(), "0::"+reader+"\n")
	writeProc(t, root, peerPID, "0::"+opsUnit+"\n")
	f := &fakeOps{}
	ops := f.ops()
	ops.cgroupOf = func(pid int) (string, error) { return ProcCgroupAt(root, pid) }
	if err := checkPeerExactWith(oneConn(t), opsUnit, ops); err != nil {
		t.Errorf("the peer in the unit was refused: %v", err)
	}
	err := checkPeerExactWith(oneConn(t), reader, ops)
	if err == nil || !strings.Contains(err.Error(), "not in") {
		t.Errorf("want = the reader's own cgroup admitted a peer that is not in it: err = %v", err)
	}
}

// A failure at any step stops there: after no pidfd, nothing else is asked, in
// particular no pid from SO_PEERCRED or anything else (never a fallback, whose
// pid can be recycled). The calls made are exactly the prefix.
func TestEachFailureStopsTheChainAtThatStep(t *testing.T) {
	boom := errors.New("boom")
	for name, c := range map[string]struct {
		f    fakeOps
		want string
	}{
		"no pidfd":   {fakeOps{pidfdErr: boom}, "pidfd"},
		"no pid":     {fakeOps{pidErr: boom}, "pidfd,pid"},
		"no cgroup":  {fakeOps{cgroupErr: boom}, "pidfd,pid,cgroup"},
		"peer gone":  {fakeOps{cgroup: opsUnit, aliveErr: boom}, "pidfd,pid,cgroup,alive"},
		"wrong unit": {fakeOps{cgroup: "/elsewhere"}, "pidfd,pid,cgroup,alive"},
	} {
		f := c.f
		if err := checkPeerExactWith(oneConn(t), opsUnit, f.ops()); err == nil {
			t.Errorf("%s: admitted", name)
		}
		if got := strings.Join(f.calls, ","); got != c.want {
			t.Errorf("%s: calls = %s, want %s", name, got, c.want)
		}
	}
}

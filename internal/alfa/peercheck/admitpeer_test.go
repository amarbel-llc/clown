package peercheck

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

// AdmitPeer is the listener's one call: the cgroup file is read, the peer is
// taken from the connection, and admission is strict equality. Against the real
// kernel with a child process in this test's own cgroup: admitted when the file
// holds that cgroup, refused when it holds another path, refused with no file.
// (A peer in ANOTHER cgroup is the live proof, not a unit test: the child here
// shares ours.)
func TestAdmitPeerReadsTheFileAndTheConnection(t *testing.T) {
	srv, _, _ := startPeer(t)
	me, err := ProcCgroup(os.Getpid())
	if errors.Is(err, ErrNoCgroupV2) {
		t.Skip("no cgroup v2 entry:", err)
	} else if err != nil {
		t.Fatalf("reading our own cgroup: %v", err)
	}
	file := filepath.Join(t.TempDir(), bridgewire.CgroupFileName)
	if err := bridgewire.WriteCgroupFile(file, me); err != nil {
		t.Fatal(err)
	}
	err = AdmitPeer(srv, file)
	if errors.Is(err, unix.ENOPROTOOPT) {
		t.Skip("kernel without SO_PEERPIDFD:", err)
	}
	if err != nil {
		t.Fatalf("a peer in the recorded cgroup was refused: %v", err)
	}
	if err := bridgewire.WriteCgroupFile(file, me+"/elsewhere"); err != nil {
		t.Fatal(err)
	}
	if err := AdmitPeer(srv, file); err == nil {
		t.Error("a peer outside the recorded cgroup was admitted")
	}
	if err := AdmitPeer(srv, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing cgroup file admitted a peer")
	}
}

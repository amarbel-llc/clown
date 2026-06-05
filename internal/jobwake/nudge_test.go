package jobwake

import (
	"os"
	"strings"
	"testing"
	"time"
)

// shortRuntimeDir returns a tempdir under /tmp rather than t.TempDir(), whose
// path under a deep worktree checkout can exceed AF_UNIX's ~108-byte sun_path
// limit. The unixgram socket lives at <dir>/clown/jobs/<cid>.sock, so the base
// must stay short. Mirrors the real $XDG_RUNTIME_DIR (/run/user/<uid>).
func shortRuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "jobwake-rt-")
	if err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestNudgeRoundTrip(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	cid := "chan"
	conn, err := bindNudge(cid)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sendNudge(cid, "job1", TypeSucceeded)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 512)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(buf[:n])); got != "1|job1|succeeded" {
		t.Fatalf("want 1|job1|succeeded, got %q", got)
	}
}

func TestSendNudgeNoListenerIsSilent(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	sendNudge("nochan", "j", TypeFailed) // must not panic / must not block
}

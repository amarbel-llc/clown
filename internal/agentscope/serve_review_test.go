package agentscope

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F12 review follow-up (FDR 0020).

// A second bridge for the same principal (a resume, a pinned
// CLOWN_SESSION_ID) must not unlink the first session's live socket.
func TestServeSocketRefusesALiveSocketAndReplacesAStaleOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve", "p1", "spinclass.sock")
	first, err := ListenServeSocket(path, "u-1.service", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ListenServeSocket(path, "u-2.service", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("second listen on a live socket: err = %v, want a refusal naming it live", err)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the first session's socket was unlinked: %v", err)
	}
	c.Close()
	first.Close()

	// A stale socket file (its listener gone, the inode left behind).
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket file missing: %v", err)
	}
	again, err := ListenServeSocket(path, "u-1.service", nil, nil, nil)
	if err != nil {
		t.Fatalf("a stale socket was not replaced: %v", err)
	}
	again.Close()
}

// A connection whose peer cannot be read is refused: the check fails closed.
func TestServePeerCheckFailsClosedWhenThePeerIsUnreadable(t *testing.T) {
	a, b := net.Pipe() // not a unix connection: no SO_PEERCRED
	defer a.Close()
	defer b.Close()
	err := authorizeServePeer(a, "u-1.service", func() int { return os.Getpid() }, func(int) (string, error) { return unitCgroup, nil })
	if err == nil {
		t.Fatal("an unreadable peer was admitted")
	}
	if !strings.Contains(err.Error(), "cgroup unknown") {
		t.Errorf("refusal %q does not say the cgroup is unknown", err)
	}
}

// The frontend is admitted by its pid as of each accept, so a process that
// later takes a dead frontend's pid is not.
func TestServePeerChecksTheFrontendPidAtEachAccept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve", "p1", "spinclass.sock")
	if serveOnce(t, path, "u-1.service", os.Getpid()+1, "/user.slice/user-1001.slice/session-3.scope") {
		t.Error("a caller whose pid is not the current frontend's was admitted")
	}
}

// spinclass reads the result with plain json.Unmarshal: the key is
// serve_socket.
func TestWrittenResultSpellsServeSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-unit.json")
	if err := WriteResult(path, Result{Unit: "u-1.service", ServeSocket: "/run/user/1001/clown/serve/p1/spinclass.sock"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["serve_socket"] != "/run/user/1001/clown/serve/p1/spinclass.sock" {
		t.Errorf("serve_socket = %v in %s", got["serve_socket"], raw)
	}
	caps, _ := got["capabilities"].([]any)
	found := false
	for _, c := range caps {
		found = found || c == "frontend-serve-socket"
	}
	if !found {
		t.Errorf("written capabilities %v lack frontend-serve-socket", caps)
	}
}

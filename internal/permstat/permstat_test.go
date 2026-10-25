package permstat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/toolcost"
)

// ev builds a hook payload as Claude Code 2.1.308 sends it (the fields
// recorded by `just explore-permission-hook-events`).
func ev(event, tool string, input string) []byte {
	m := map[string]any{"hook_event_name": event, "session_id": "s-1", "prompt_id": "p-1", "permission_mode": "default"}
	if tool != "" {
		m["tool_name"] = tool
		m["tool_input"] = json.RawMessage(input)
	}
	b, _ := json.Marshal(m)
	return b
}

// replay feeds payloads through Handle one at a time, the state going
// through its file in between, the way separate hook processes see it.
func replay(t *testing.T, payloads ...[]byte) []string {
	t.Helper()
	dir := t.TempDir()
	var lines []string
	for _, p := range payloads {
		ms, err := Handle(p, dir)
		if err != nil {
			t.Logf("handle: %v", err)
		}
		lines = append(lines, toolcost.Encode(ms)...)
	}
	return lines
}

const bash = `{"command":"touch X","description":"Create empty file X"}`

// Recorded sequence, case approve: PreToolUse, PermissionRequest,
// PostToolUse, Stop.
func TestPromptedThenApproved(t *testing.T) {
	got := replay(t,
		ev("UserPromptSubmit", "", ""),
		ev("PreToolUse", "Bash", bash),
		ev("PermissionRequest", "Bash", bash),
		ev("PostToolUse", "Bash", bash),
		ev("Stop", "", ""),
	)
	want := []string{"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.approved:1|c"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

// A failed tool still ran, so the permission was granted.
func TestPromptedThenFailedCountsAsApproved(t *testing.T) {
	got := replay(t,
		ev("PermissionRequest", "Bash", bash),
		ev("PostToolUseFailure", "Bash", bash),
		ev("Stop", "", ""),
	)
	if !slices.Contains(got, "clown.permission.builtin.Bash.approved:1|c") {
		t.Fatalf("lines = %q", got)
	}
}

// Recorded sequences, cases default and reject: PermissionRequest and no
// follow-up before the turn ends: declined (rejected or interrupted; hooks
// cannot tell them apart).
func TestPromptedThenDeclined(t *testing.T) {
	for _, end := range []string{"Stop", "UserPromptSubmit", "SessionEnd"} {
		got := replay(t,
			ev("PreToolUse", "Bash", bash),
			ev("PermissionRequest", "Bash", bash),
			ev(end, "", ""),
		)
		want := []string{"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.declined:1|c"}
		if !slices.Equal(got, want) {
			t.Fatalf("ended by %s: lines = %q, want %q", end, got, want)
		}
	}
}

// Recorded sequence, case auto: no PermissionRequest, no counter.
func TestAutoAllowedEmitsNothing(t *testing.T) {
	if got := replay(t,
		ev("UserPromptSubmit", "", ""),
		ev("PreToolUse", "Read", `{"file_path":"inside.txt"}`),
		ev("PostToolUse", "Read", `{"file_path":"inside.txt"}`),
		ev("Stop", "", ""),
	); len(got) != 0 {
		t.Fatalf("lines = %q, want none", got)
	}
}

// A PreToolUse of another call in between (parallel tool calls) does not
// decline a pending request: only a turn boundary does.
func TestAnInterleavedCallDoesNotDecline(t *testing.T) {
	got := replay(t,
		ev("PermissionRequest", "Bash", bash),
		ev("PreToolUse", "Read", `{"file_path":"a"}`),
		ev("PostToolUse", "Read", `{"file_path":"a"}`),
		ev("PostToolUse", "Bash", bash),
		ev("Stop", "", ""),
	)
	want := []string{"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.approved:1|c"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

// MCP tool names map as clown.tool_result.* does (ToolKey).
func TestMCPToolKey(t *testing.T) {
	got := replay(t, ev("PermissionRequest", "mcp__plugin_moxy_moxy__rg_search", `{"pattern":"x"}`))
	if !slices.Equal(got, []string{"clown.permission.moxy.rg_search.prompted:1|c"}) {
		t.Fatalf("lines = %q", got)
	}
}

// Two identical requests in one turn, one of which runs: one approved, one
// declined (the pairing cannot tell which; the counts are still right).
func TestRepeatedIdenticalCalls(t *testing.T) {
	got := replay(t,
		ev("PermissionRequest", "Bash", bash),
		ev("PermissionRequest", "Bash", bash),
		ev("PostToolUse", "Bash", bash),
		ev("Stop", "", ""),
	)
	want := []string{"clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.prompted:1|c", "clown.permission.builtin.Bash.approved:1|c", "clown.permission.builtin.Bash.declined:1|c"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

// Malformed input or an event this does not track: no metrics, an error at
// most, never a panic.
func TestMalformedInputEmitsNothing(t *testing.T) {
	dir := t.TempDir()
	for _, in := range [][]byte{nil, []byte("not json"), []byte(`{"hook_event_name":"PermissionRequest"}`), []byte(`{"hook_event_name":"Notification","session_id":"s"}`)} {
		if ms, _ := Handle(in, dir); len(ms) != 0 {
			t.Errorf("Handle(%q) = %v, want nothing", in, ms)
		}
	}
}

// The state file: 0600 in a 0700 dir, removed at the end of the turn,
// corruption tolerated, a symlink at its path refused.
func TestStateFileHygiene(t *testing.T) {
	dir := t.TempDir()
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	path := statePath(dir, "s-1")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v, want 0600", fi, err)
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode %v, want 0700", di.Mode().Perm())
	}
	if _, err := Handle(ev("Stop", "", ""), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file must be removed at the end of the turn: %v", err)
	}

	// Corrupt: treated as empty, the request still counts.
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	ms, _ := Handle(ev("PermissionRequest", "Bash", bash), dir)
	if len(ms) != 1 || !strings.HasSuffix(ms[0].Name, ".prompted") {
		t.Fatalf("after corruption: %v", ms)
	}

	// A symlink planted at the path is not followed.
	os.Remove(path)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err == nil {
		t.Fatal("a symlinked state file must be refused with an error")
	}
	if b, _ := os.ReadFile(victim); len(b) != 0 {
		t.Fatalf("wrote through a symlink: %q", b)
	}
}

// The pending list is bounded: a session that never resolves cannot grow
// the file without limit.
func TestPendingIsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3*maxPending; i++ {
		if _, err := Handle(ev("PermissionRequest", "Bash", `{"command":"echo `+strings.Repeat("x", i)+`"}`), dir); err != nil {
			t.Fatal(err)
		}
	}
	st := loadState(statePath(dir, "s-1"))
	if len(st.Pending) != maxPending {
		t.Fatalf("pending = %d, want exactly %d", len(st.Pending), maxPending)
	}
}

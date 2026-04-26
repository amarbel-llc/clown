package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscript creates a synthetic Claude JSONL transcript with a head
// entry carrying cwd/gitBranch and an optional tail entry carrying a title.
func writeTranscript(t *testing.T, path, cwd, gitBranch, title string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if cwd != "" || gitBranch != "" {
		_ = enc.Encode(map[string]any{
			"type":      "user",
			"cwd":       cwd,
			"gitBranch": gitBranch,
		})
	}
	if title != "" {
		_ = enc.Encode(map[string]any{
			"type":        "custom-title",
			"customTitle": title,
		})
	}
	f.Close()
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListClaudeSessions_EnumeratesAndSortsByMtimeDesc(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")

	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)

	writeTranscript(t,
		filepath.Join(projects, "-tmp-foo", "abc.jsonl"),
		"/tmp/foo", "main", "older session", older)
	writeTranscript(t,
		filepath.Join(projects, "-tmp-bar", "def.jsonl"),
		"/tmp/bar", "feat", "newer session", newer)

	got, err := ListClaudeSessions(home)
	if err != nil {
		t.Fatalf("ListClaudeSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	if got[0].ID != "def" {
		t.Errorf("first session id = %q, want def (newer mtime)", got[0].ID)
	}
	if got[1].ID != "abc" {
		t.Errorf("second session id = %q, want abc", got[1].ID)
	}
	if got[0].CWD != "/tmp/bar" {
		t.Errorf("first session cwd = %q, want /tmp/bar", got[0].CWD)
	}
	if got[0].Title != "newer session" {
		t.Errorf("first session title = %q, want newer session", got[0].Title)
	}
	if got[0].GitBranch != "feat" {
		t.Errorf("first session gitBranch = %q, want feat", got[0].GitBranch)
	}
}

func TestListClaudeSessions_MissingProjectsDir(t *testing.T) {
	home := t.TempDir()
	got, err := ListClaudeSessions(home)
	if err != nil {
		t.Fatalf("ListClaudeSessions on empty home: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d sessions, want 0", len(got))
	}
}

func TestListClaudeSessions_SkipsNonJSONLFiles(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(filepath.Join(projects, "-tmp-foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "-tmp-foo", "todo.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t,
		filepath.Join(projects, "-tmp-foo", "abc.jsonl"),
		"/tmp/foo", "", "", time.Now())

	got, err := ListClaudeSessions(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "abc" {
		t.Errorf("got %+v, want one session id=abc", got)
	}
}

func TestListClaudeSessions_HandlesMissingHeadFields(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	// transcript with no cwd in head — should still be enumerated, just with empty CWD
	writeTranscript(t,
		filepath.Join(projects, "-tmp-x", "no-cwd.jsonl"),
		"", "", "untitled", time.Now())

	got, err := ListClaudeSessions(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if got[0].CWD != "" {
		t.Errorf("expected empty CWD when transcript has no cwd field, got %q", got[0].CWD)
	}
}

func TestFilterByCWD_ExactMatch(t *testing.T) {
	all := []Session{
		{ID: "a", CWD: "/tmp/foo"},
		{ID: "b", CWD: "/tmp/bar"},
		{ID: "c", CWD: "/tmp/foo"},
		{ID: "d", CWD: "/tmp/foo/sub"},
		{ID: "e", CWD: ""},
	}
	got := FilterByCWD(all, "/tmp/foo")
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (exact-match only)", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("ids = [%s %s], want [a c]", got[0].ID, got[1].ID)
	}
}

func TestFilterByCWD_NoMatchReturnsEmpty(t *testing.T) {
	all := []Session{{ID: "a", CWD: "/tmp/foo"}}
	got := FilterByCWD(all, "/somewhere/else")
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

func TestExtractTitle_PrefersCustomTitleOverAgentName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	_ = enc.Encode(map[string]any{"type": "agent-name", "agentName": "agent-thing"})
	_ = enc.Encode(map[string]any{"type": "custom-title", "customTitle": "the real title"})
	f.Close()

	got := extractTitle(path)
	if got != "the real title" {
		t.Errorf("title = %q, want %q", got, "the real title")
	}
}

func TestExtractTitle_FallsBackToAgentName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	_ = enc.Encode(map[string]any{"type": "agent-name", "agentName": "agent-thing"})
	f.Close()

	got := extractTitle(path)
	if got != "agent-thing" {
		t.Errorf("title = %q, want %q", got, "agent-thing")
	}
}

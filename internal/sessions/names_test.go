package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordSessionNameRoundtrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := RecordSessionName("id-1", "bozo", "repo/wt"); err != nil {
		t.Fatalf("RecordSessionName: %v", err)
	}
	if got := NameOf("id-1"); got != "bozo" {
		t.Errorf("NameOf(id-1) = %q, want bozo", got)
	}
	if got := NameOf("id-absent"); got != "" {
		t.Errorf("NameOf(id-absent) = %q, want \"\"", got)
	}
}

// A resume appends a fresh record for the same id under the new live
// name; the reader must return the LAST record per id.
func TestNamesForLastRecordWins(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := RecordSessionName("id-1", "bozo", "repo/wt"); err != nil {
		t.Fatal(err)
	}
	if err := RecordSessionName("id-1", "krusty", "repo/wt"); err != nil {
		t.Fatal(err)
	}
	if got := NameOf("id-1"); got != "krusty" {
		t.Errorf("NameOf(id-1) = %q, want krusty (last record wins)", got)
	}
}

func TestNamesForScopesToRequestedIDs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := RecordSessionName("id-1", "bozo", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordSessionName("id-2", "krusty", ""); err != nil {
		t.Fatal(err)
	}
	got := NamesFor([]string{"id-2", "id-3"})
	if len(got) != 1 || got["id-2"] != "krusty" {
		t.Errorf("NamesFor = %v, want map[id-2:krusty]", got)
	}
}

// A machine with no sidecar yet must degrade to "no names", not error.
func TestNamesForMissingSidecar(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if got := NamesFor([]string{"id-1"}); len(got) != 0 {
		t.Errorf("NamesFor on missing sidecar = %v, want empty", got)
	}
}

// Empty id or name is a silent no-op — nothing meaningful to record, and
// the writer must not create garbage lines.
func TestRecordSessionNameEmptyNoOp(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	if err := RecordSessionName("", "bozo", ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordSessionName("id-1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "clown", "session-names.jsonl")); !os.IsNotExist(err) {
		t.Errorf("sidecar must not exist after no-op writes (stat err = %v)", err)
	}
}

// A corrupt or oversized line (e.g. a partial write from a crash) must not
// blind NamesFor to records recorded AFTER it in this never-pruned,
// append-only file — only that one line should be lost. Regression test for
// a bufio.Scanner-based reader that stopped scanning entirely once it hit a
// line past its max token size.
func TestNamesForSurvivesOversizedCorruptLine(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	if err := RecordSessionName("id-before", "bozo", ""); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(state, "clown", "session-names.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", 2*1024*1024) // exceeds bufio.Scanner's default 1MB max token size
	if _, err := f.WriteString(huge + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := RecordSessionName("id-after", "krusty", ""); err != nil {
		t.Fatal(err)
	}

	got := NamesFor([]string{"id-before", "id-after"})
	if got["id-before"] != "bozo" {
		t.Errorf("id-before = %q, want bozo (record before the corrupt line)", got["id-before"])
	}
	if got["id-after"] != "krusty" {
		t.Errorf("id-after = %q, want krusty — a record AFTER a corrupt/oversized line must still be readable", got["id-after"])
	}
}

// The group rides along in the record (future queries; step 3 only reads
// names, but the journal is append-only so the schema must be right now).
func TestRecordSessionNamePersistsGroup(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	if err := RecordSessionName("id-1", "bozo", "repo/wt"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(state, "clown", "session-names.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"group":"repo/wt"`) {
		t.Errorf("sidecar line missing group: %s", b)
	}
}

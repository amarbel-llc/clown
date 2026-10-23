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

// FDR 0022 slice 1 (clown#295): a launch records the profile name and
// digest it was launched with; ProfileOf returns the last record that
// carries one.
func TestProfileOf(t *testing.T) {
	cases := []struct {
		name    string
		records []NameRecord
		lookup  string
		want    ProfilePin
		wantOK  bool
	}{
		{
			name:   "no sidecar",
			lookup: "id-1",
		},
		{
			name:    "record without profile",
			records: []NameRecord{{SessionID: "id-1", Name: "bozo"}},
			lookup:  "id-1",
		},
		{
			name:    "record with profile",
			records: []NameRecord{{SessionID: "id-1", Name: "bozo", Profile: "claude-opus", ProfileDigest: "aaa"}},
			lookup:  "id-1",
			want:    ProfilePin{Name: "claude-opus", Digest: "aaa"},
			wantOK:  true,
		},
		{
			name: "last profile record wins",
			records: []NameRecord{
				{SessionID: "id-1", Name: "bozo", Profile: "p1", ProfileDigest: "aaa"},
				{SessionID: "id-1", Name: "bozo", Profile: "p2", ProfileDigest: "bbb"},
			},
			lookup: "id-1",
			want:   ProfilePin{Name: "p2", Digest: "bbb"},
			wantOK: true,
		},
		{
			// A later launch that applied no profile must not erase the pin:
			// a session's profile never changes silently.
			name: "profileless record after a profile record keeps the pin",
			records: []NameRecord{
				{SessionID: "id-1", Name: "bozo", Profile: "p1", ProfileDigest: "aaa"},
				{SessionID: "id-1", Name: "krusty"},
			},
			lookup: "id-1",
			want:   ProfilePin{Name: "p1", Digest: "aaa"},
			wantOK: true,
		},
		{
			name:    "other session's profile is not returned",
			records: []NameRecord{{SessionID: "id-2", Name: "bozo", Profile: "p1", ProfileDigest: "aaa"}},
			lookup:  "id-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			for _, r := range tc.records {
				if err := RecordSession(r); err != nil {
					t.Fatalf("RecordSession: %v", err)
				}
			}
			got, ok := ProfileOf(tc.lookup)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("ProfileOf(%q) = %+v, %v; want %+v, %v", tc.lookup, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// A record written before FDR 0022 (no profile keys) still parses, and a
// record carrying a profile still serves name lookups.
func TestSidecarProfileFieldsCompatible(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	dir := filepath.Join(state, "clown")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"session_id":"id-old","name":"bozo","group":"r/w","ts":"2026-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session-names.jsonl"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-new", Name: "krusty", Profile: "p1", ProfileDigest: "aaa"}); err != nil {
		t.Fatal(err)
	}

	if got := NameOf("id-old"); got != "bozo" {
		t.Errorf("NameOf(id-old) = %q, want bozo", got)
	}
	if _, ok := ProfileOf("id-old"); ok {
		t.Error("ProfileOf(id-old) found a profile in a pre-FDR-0022 record")
	}
	if got := NameOf("id-new"); got != "krusty" {
		t.Errorf("NameOf(id-new) = %q, want krusty", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, "session-names.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"profile":"p1","profile_digest":"aaa"`) {
		t.Errorf("sidecar line missing profile fields: %s", b)
	}

	if err := RecordSessionName("id-plain", "sideshow", ""); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "session-names.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if last := lines[len(lines)-1]; strings.Contains(last, "profile") {
		t.Errorf("profileless record must omit the profile keys: %s", last)
	}
}

// A profile-only record (no clown-name) is still written, and must not
// blank the name an earlier record bound.
func TestProfileOnlyRecordKeepsName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := RecordSession(NameRecord{SessionID: "id-1", Name: "bozo"}); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-1", Profile: "p1", ProfileDigest: "aaa"}); err != nil {
		t.Fatal(err)
	}
	if got := NameOf("id-1"); got != "bozo" {
		t.Errorf("NameOf(id-1) = %q, want bozo", got)
	}
	if got, ok := ProfileOf("id-1"); !ok || got.Name != "p1" {
		t.Errorf("ProfileOf(id-1) = %+v, %v; want p1", got, ok)
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

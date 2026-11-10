package catalogsnap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// writeAged writes a snapshot as Write would, but stamped at writtenAt.
func writeAged(t *testing.T, dir, principal string, writtenAt time.Time) {
	t.Helper()
	b, err := json.Marshal(Snapshot{Version: Version, Principal: principal, WrittenAt: writtenAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName(principal)), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, principal string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName(principal)))
	return err == nil
}

func TestPruneRemovesOnlyDeadSnapshotsOlderThanTheRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-Retention - time.Hour)
	recent := now.Add(-Retention + time.Hour)
	writeAged(t, dir, "live-old", old)
	writeAged(t, dir, "dead-recent", recent)
	writeAged(t, dir, "dead-old", old)
	alive := func(s Snapshot) bool { return s.Principal == "live-old" }

	removed := Prune(dir, now, Retention, alive)

	if !slices.Equal(removed, []string{"dead-old"}) {
		t.Errorf("Prune removed %v, want [dead-old]", removed)
	}
	for p, want := range map[string]bool{"live-old": true, "dead-recent": true, "dead-old": false} {
		if exists(dir, p) != want {
			t.Errorf("%s on disk = %v, want %v", p, !want, want)
		}
	}
}

// A file Prune cannot read as a snapshot, or one with no time, is left alone:
// it never deletes what it cannot date.
func TestPruneLeavesWhatItCannotDate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Snapshot{Version: Version, Principal: "undated"})
	if err := os.WriteFile(filepath.Join(dir, FileName("undated")), b, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if removed := Prune(dir, now, Retention, func(Snapshot) bool { return false }); len(removed) != 0 {
		t.Errorf("Prune removed %v", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk.json")); err != nil {
		t.Errorf("junk.json removed: %v", err)
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clown#225: cursor files of transcripts that will never be appended to again
// are pruned by age, opportunistically, on a hook run.
func TestPruneStaleCursorsRemovesOnlyOldCursorFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-(cursorMaxAge + 24*time.Hour))
	files := map[string]time.Time{
		"old.cursor":     old,
		"old.cursor.tmp": old,
		"fresh.cursor":   now.Add(-time.Hour),
		"current.cursor": old, // the transcript this run is posting for
		"unrelated.txt":  old,
	}
	for name, mtime := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	removed := pruneStaleCursors(dir, now, cursorMaxAge, filepath.Join(dir, "current.cursor"))
	if removed != 2 {
		t.Errorf("removed %d files, want 2", removed)
	}
	for name, wantGone := range map[string]bool{"old.cursor": true, "old.cursor.tmp": true, "fresh.cursor": false, "current.cursor": false, "unrelated.txt": false} {
		_, err := os.Stat(filepath.Join(dir, name))
		if gone := os.IsNotExist(err); gone != wantGone {
			t.Errorf("%s: gone = %v, want %v", name, gone, wantGone)
		}
	}
	if n := pruneStaleCursors(filepath.Join(dir, "missing"), now, cursorMaxAge, ""); n != 0 {
		t.Errorf("a missing directory pruned %d", n)
	}
}

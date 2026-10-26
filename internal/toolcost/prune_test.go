package toolcost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, path string, age time.Duration, now time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := now.Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestPruneStateRemovesOnlyOldStateAndLocks(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old, fresh, kept := filepath.Join(dir, "old.json"), filepath.Join(dir, "fresh.json"), filepath.Join(dir, "kept.json")
	touch(t, old, StateMaxAge+time.Hour, now)
	touch(t, old+".lock", StateMaxAge+time.Hour, now)
	touch(t, fresh, StateMaxAge-time.Hour, now)
	touch(t, fresh+".lock", StateMaxAge-time.Hour, now)
	touch(t, kept, 2*StateMaxAge, now)
	touch(t, kept+".lock", 2*StateMaxAge, now)
	other := filepath.Join(dir, "notes.txt")
	touch(t, other, 2*StateMaxAge, now)

	if got := PruneState(dir, StateMaxAge, now, kept); got != 2 {
		t.Fatalf("removed %d, want 2 (old.json and its lock)", got)
	}
	for _, p := range []string{old, old + ".lock"} {
		if exists(p) {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{fresh, fresh + ".lock", kept, kept + ".lock", other} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
}

func TestPruneStateMissingDirIsHarmless(t *testing.T) {
	if got := PruneState(filepath.Join(t.TempDir(), "absent"), StateMaxAge, time.Now(), ""); got != 0 {
		t.Fatalf("removed %d from a missing dir", got)
	}
}

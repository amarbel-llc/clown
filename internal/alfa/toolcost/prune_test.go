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
	// A live transcript's lock is old (never rewritten) while its state is
	// fresh: it must survive, or a second run could take a fresh lock inode.
	live := filepath.Join(dir, "live.json")
	touch(t, live, time.Hour, now)
	touch(t, live+".lock", 2*StateMaxAge, now)
	// An orphan old lock (no state file) goes.
	orphan := filepath.Join(dir, "orphan.json.lock")
	touch(t, orphan, 2*StateMaxAge, now)
	other := filepath.Join(dir, "notes.txt")
	touch(t, other, 2*StateMaxAge, now)

	if got := PruneState(dir, StateMaxAge, now, kept); got != 3 {
		t.Fatalf("removed %d, want 3 (old.json, its lock, the orphan lock)", got)
	}
	for _, p := range []string{old, old + ".lock", orphan} {
		if exists(p) {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{fresh, fresh + ".lock", kept, kept + ".lock", live, live + ".lock", other} {
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

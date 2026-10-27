package main

import (
	"os"
	"testing"
	"time"
)

func ledgerFile(t *testing.T, session, moxin string, age time.Duration, now time.Time) string {
	t.Helper()
	path, err := moxinLedgerPath(session, moxin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".lock"} {
		if err := os.WriteFile(p, []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func present(path string) bool { _, err := os.Lstat(path); return err == nil }

// The per-session call counts are throwaway: a ledger (and its lock) that no
// call has touched for longer than moxinLedgerMaxAge, far longer than the cap's
// window, goes; a recent one and the one the proxy is about to use stay.
func TestMoxinLedgersAreRetained(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Now()
	old := ledgerFile(t, "session-old", "issue-filer", moxinLedgerMaxAge+time.Hour, now)
	recent := ledgerFile(t, "session-recent", "issue-filer", moxinLedgerMaxAge-time.Hour, now)
	mine := ledgerFile(t, "session-mine", "issue-filer", 2*moxinLedgerMaxAge, now)

	if moxinLedgerMaxAge <= 24*moxinCapWindow {
		t.Fatalf("retention %v must be far longer than the cap window %v", moxinLedgerMaxAge, moxinCapWindow)
	}
	if got := pruneMoxinLedgers(mine, now); got != 2 {
		t.Fatalf("removed %d files, want 2 (the old ledger and its lock)", got)
	}
	for _, p := range []string{old, old + ".lock"} {
		if present(p) {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{recent, recent + ".lock", mine, mine + ".lock"} {
		if !present(p) {
			t.Errorf("%s was removed", p)
		}
	}
}

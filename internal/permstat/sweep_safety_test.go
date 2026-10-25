package permstat

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Final review of 6697671 (clown#302): what the sweep does with entries that
// carry a state-file name but are not regular files. A state file is never
// anything but regular, so such an entry is planted: it is removed (the entry
// itself, through the root, never what it points at), which stops planted
// names piling up; a directory is left alone.
func TestSweepRemovesStateNamedSpecialFilesButNotTheirTargets(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim.json")
	if err := os.WriteFile(outside, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, stateName(40))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, stateName(41))
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, stateName(42))
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Handle(ev("PermissionRequest", "Bash", bash), dir)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the sweep blocked on a state-named FIFO")
	}
	for _, p := range []string{link, fifo} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("state-named special file %s was not removed: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the symlink's target was touched: %v", err)
	}
	if fi, err := os.Lstat(sub); err != nil || !fi.IsDir() {
		t.Errorf("a state-named directory was not left alone: %v", err)
	}
}

// A dir an agent filled with thousands of state-named files that hold a
// pending entry and are a few days old (so each needs a read to tell it from
// an empty one), plus oversized ones: one sweep reads at most maxSweepLoads
// of them and removes the oversized ones unread. Uses the sweepLoadHook seam
// added with the budget, so written with the fix and never red.
func TestOneSweepDoesBoundedWork(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3000; i++ {
		writeAged(t, filepath.Join(dir, stateName(10000+i)), 1, 48*time.Hour)
	}
	var oversized []string
	for i := 0; i < 10; i++ {
		p := filepath.Join(dir, stateName(20000+i))
		writeAged(t, p, 1, 48*time.Hour)
		// Sparse: past maxStateBytes without using the disk.
		if err := os.Truncate(p, maxStateBytes+1); err != nil {
			t.Fatal(err)
		}
		oversized = append(oversized, p)
	}
	loads := 0
	sweepLoadHook = func() { loads++ }
	t.Cleanup(func() { sweepLoadHook = nil })
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	if loads > maxSweepLoads {
		t.Errorf("one sweep read %d state files, want at most %d", loads, maxSweepLoads)
	}
	for _, p := range oversized {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("oversized %s was not removed: %v", filepath.Base(p), err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The 3000 pending files stay (2 days old, under the 7-day rule), plus
	// this session's own file.
	if len(entries) != 3001 {
		t.Errorf("%d entries left, want 3001", len(entries))
	}
}

// A FIFO (or anything not regular) at a session's state-file name is
// refused at once with a clear error, not retried as if it had been
// replaced; the prompt still counts.
func TestANonRegularStateFileIsRefusedAtOnce(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(statePath(dir, "s-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	done := make(chan struct{})
	var ms int
	var err error
	go func() {
		got, e := Handle(ev("PermissionRequest", "Bash", bash), dir)
		ms, err = len(got), e
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("PermissionRequest blocked on a FIFO state file")
	}
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file error", err)
	}
	if ms != 1 {
		t.Fatalf("PermissionRequest metrics = %d, want prompted still counted", ms)
	}
	if took := time.Since(start); took > 2*lockWait {
		t.Fatalf("took %v: retried instead of refusing at once", took)
	}
}

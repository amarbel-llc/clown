package permstat

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// replaceAt renames a fresh copy of the state file over path, the way a turn
// boundary's remove plus the next request's create looks to a waiting hook.
func replaceAt(t *testing.T, path string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(path), "replacement.tmp")
	if err := os.WriteFile(tmp, []byte(`{"version":1}`), 0o600); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Error(err)
	}
}

func withAfterLock(t *testing.T, hook func()) {
	t.Helper()
	afterLockHook = hook
	t.Cleanup(func() { afterLockHook = nil })
}

// A file replaced after every lock: the attempts run out with an error, and
// the prompt still counts.
func TestReopenAttemptsRunOut(t *testing.T) {
	dir := t.TempDir()
	path := statePath(dir, "s-1")
	attempts := 0
	withAfterLock(t, func() {
		attempts++
		replaceAt(t, path)
	})
	ms, err := Handle(ev("PermissionRequest", "Bash", bash), dir)
	if err == nil || !strings.Contains(err.Error(), "kept being replaced") {
		t.Fatalf("err = %v, want the attempts to run out", err)
	}
	if attempts != maxReopen {
		t.Fatalf("attempts = %d, want %d", attempts, maxReopen)
	}
	if len(ms) != 1 || !strings.HasSuffix(ms[0].Name, ".prompted") {
		t.Fatalf("ms = %v, want prompted still counted", ms)
	}
}

// A file replaced once: the second attempt works on the file the path names.
func TestReopenOnceThenWrite(t *testing.T) {
	dir := t.TempDir()
	path := statePath(dir, "s-1")
	attempts := 0
	withAfterLock(t, func() {
		attempts++
		if attempts == 1 {
			replaceAt(t, path)
		}
	})
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if st := loadState(path); len(st.Pending) != 1 {
		t.Fatalf("pending at the path = %d, want 1", len(st.Pending))
	}
}

// Every attempt finds the file replaced and the new one locked for 150ms:
// all attempts share one lockWait deadline, so the hook gives up near
// lockWait, not near maxReopen*lockWait.
func TestReopenAttemptsShareOneDeadline(t *testing.T) {
	dir := t.TempDir()
	path := statePath(dir, "s-1")
	var holders []*os.File
	t.Cleanup(func() {
		for _, f := range holders {
			f.Close()
		}
	})
	withAfterLock(t, func() {
		replaceAt(t, path)
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Error(err)
			return
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			t.Error(err)
		}
		holders = append(holders, f)
		go func() {
			time.Sleep(150 * time.Millisecond)
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}()
	})
	start := time.Now()
	_, err := Handle(ev("PermissionRequest", "Bash", bash), dir)
	took := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "locked for over") {
		t.Fatalf("err = %v, want a lock timeout", err)
	}
	if took > 2*lockWait {
		t.Fatalf("took %v, want about %v (one deadline for all attempts)", took, lockWait)
	}
}

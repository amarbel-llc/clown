package permstat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clown#302: the state lives under $XDG_STATE_HOME/clown/hook-toolcost/
// permstat, the toolcost hook's own state dir, which an --agent-scope unit
// binds read-write at the same path. It does not depend on XDG_RUNTIME_DIR,
// which is a read-only tmpfs inside a unit.
func TestDirIsUnderTheHookToolcostStateDir(t *testing.T) {
	state, runtime := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	if got, want := Dir(), filepath.Join(state, "clown", "hook-toolcost", "permstat"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}

	// Without XDG_STATE_HOME, the same ladder as the toolcost hook
	// (userpath.StatePath): ~/.local/state.
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	if got, want := Dir(), filepath.Join(home, ".local", "state", "clown", "hook-toolcost", "permstat"); got != want {
		t.Errorf("Dir() without XDG_STATE_HOME = %q, want %q", got, want)
	}
}

// The new dir survives a reboot, so the stale sweep runs there (empty past
// 24h, pending past 7 days); files left in the old runtime-dir location are
// not touched (no migration: the tmpfs clears them).
func TestSweepRunsInTheStateDirAndIgnoresTheOldLocation(t *testing.T) {
	state, runtime := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(runtime, "clown", "permstat")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	emptyOld := filepath.Join(dir, stateName(1))
	pendingWeek := filepath.Join(dir, stateName(2))
	pendingAbandoned := filepath.Join(dir, stateName(3))
	leftover := filepath.Join(oldDir, stateName(4))
	writeAged(t, emptyOld, 0, 48*time.Hour)
	writeAged(t, pendingWeek, 1, 48*time.Hour)
	writeAged(t, pendingAbandoned, 1, 8*24*time.Hour)
	writeAged(t, leftover, 0, 8*24*time.Hour)

	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{emptyOld, pendingAbandoned} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was not swept: %v", filepath.Base(p), err)
		}
	}
	for _, p := range []string{pendingWeek, leftover} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was swept: %v", p, err)
		}
	}
	if _, err := os.Stat(statePath(dir, "s-1")); err != nil {
		t.Errorf("no state file in the state dir: %v", err)
	}
}

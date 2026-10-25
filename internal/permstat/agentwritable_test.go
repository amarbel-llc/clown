package permstat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/toolcost"
)

// The state dir is writable by the agent (inside --agent-scope it runs as the
// same uid) and also used by the host-side hook, so nothing read from it is
// trusted. These tests come from the review of c99acb0 (clown#302).

// The sweep removes only names a state file can have (32 hex digits and
// .json), never other JSON however old: a package.json, a toolcost state file
// (64 hex digits), anything else.
func TestSweepRemovesOnlyStateFileNames(t *testing.T) {
	dir := t.TempDir()
	others := []string{"package.json", "abc.json", fmt.Sprintf("%064x.json", 7), "0000old.json", stateName(8) + ".bak"}
	for _, n := range others {
		writeAged(t, filepath.Join(dir, n), 0, 30*24*time.Hour)
	}
	stale := filepath.Join(dir, stateName(9))
	writeAged(t, stale, 0, 30*24*time.Hour)
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err != nil {
		t.Fatal(err)
	}
	for _, n := range others {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was removed by the sweep: %v", n, err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a stale state file was not swept: %v", err)
	}
}

// writeState writes the state file of session s-1 directly, as an agent
// could.
func writeState(t *testing.T, dir string, st state) {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(dir, "s-1"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A key read back from the state file reaches statsd only if it is a metric
// key of the toolcost alphabet ([A-Za-z0-9_-] segments joined by dots);
// anything else (':', '|', a newline: statsd syntax) is dropped.
func TestKeysFromTheStateFileAreValidated(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeState(t, dir, state{Version: stateVersion, Pending: []pending{
		{Key: "builtin.Bash", Match: "m1"},
		{Key: "evil:1|c\nclown.permission.x", Match: "m2"},
		{Key: "a|b", Match: "m3"},
		{Key: "a:b", Match: "m4"},
		{Key: "", Match: "m5"},
		{Key: "a..b", Match: "m6"},
	}})
	ms, err := Handle(ev("Stop", "", ""), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolcost.Encode(ms); !slices.Equal(got, []string{"clown.permission.builtin.Bash.declined:1|c"}) {
		t.Fatalf("Stop = %q, want only the valid key declined", got)
	}
}

// At most maxPending entries are taken from a state file, however many it
// holds.
func TestTakeClampsToMaxPending(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var many []pending
	for i := 0; i < 3*maxPending; i++ {
		many = append(many, pending{Key: "builtin.Bash", Match: fmt.Sprint(i)})
	}
	writeState(t, dir, state{Version: stateVersion, Pending: many})
	ms, err := Handle(ev("Stop", "", ""), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != maxPending {
		t.Fatalf("Stop declined %d, want %d (maxPending)", len(ms), maxPending)
	}
}

// A state dir swapped for a symlink to another directory of the user's
// (holding old *.json, mode 0755) is refused; nothing there is removed or
// chmod'd.
func TestASymlinkedLeafLeavesItsTargetAlone(t *testing.T) {
	victim := t.TempDir()
	if err := os.Chmod(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"package.json", stateName(30)}
	for _, n := range names {
		writeAged(t, filepath.Join(victim, n), 0, 30*24*time.Hour)
	}
	dir := filepath.Join(t.TempDir(), "permstat")
	if err := os.Symlink(victim, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Handle(ev("PermissionRequest", "Bash", bash), dir); err == nil {
		t.Fatal("a symlinked state dir was not refused")
	}
	assertUntouched(t, victim, names)
}

func assertUntouched(t *testing.T, victim string, names []string) {
	t.Helper()
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(victim, n)); err != nil {
			t.Errorf("%s in the symlink's target was removed: %v", n, err)
		}
	}
	if fi, err := os.Stat(victim); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the symlink's target mode changed: %v %v", fi.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(victim)
	if len(entries) != len(names) {
		t.Errorf("the symlink's target holds %d entries, want %d (nothing created there)", len(entries), len(names))
	}
}

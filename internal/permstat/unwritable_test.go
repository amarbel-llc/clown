package permstat

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/toolcost"
)

// A state dir that cannot be created (as happened inside --agent-scope while
// the state lived under the unit's read-only $XDG_RUNTIME_DIR, and still
// could under a read-only XDG_STATE_HOME): prompted still counts, with an
// error from the PermissionRequest (logged by the hook, never printed);
// approved and declined do not, and their events report no error.
func TestUncreatableStateDirCountsOnlyPrompted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("needs a non-root user: root can create the dir under a read-only parent")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	dir := filepath.Join(parent, "clown", "permstat")

	ms, err := Handle(ev("PermissionRequest", "Bash", bash), dir)
	if err == nil {
		t.Fatal("PermissionRequest with an uncreatable state dir reported no error")
	}
	if got := toolcost.Encode(ms); !slices.Equal(got, []string{"clown.permission.builtin.Bash.prompted:1|c"}) {
		t.Fatalf("PermissionRequest = %q, want prompted only", got)
	}
	for _, e := range []string{"PostToolUse", "Stop", "UserPromptSubmit", "SessionEnd"} {
		ms, err := Handle(ev(e, "Bash", bash), dir)
		if err != nil || len(ms) != 0 {
			t.Fatalf("%s with no state dir = %v, %v; want nothing and no error", e, ms, err)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("state dir exists after all: %v", err)
	}
}

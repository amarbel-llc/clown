package permstat

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/toolcost"
)

// Inside an --agent-scope unit the state dir ($XDG_RUNTIME_DIR/clown/
// permstat) is not bound in, and the unit's /run/user is a tmpfs mounted
// read-only (ProtectHome=tmpfs), so it cannot be created. This pins what is
// counted then: prompted still is, with an error from the PermissionRequest
// (logged by the hook, never printed); approved and declined are not, and
// their events report no error.
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

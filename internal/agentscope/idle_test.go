package agentscope

import (
	"slices"
	"strings"
	"testing"
)

// clown#287: the frontend's idle socket is bound into the unit as a single
// inode so a Stop hook inside can poke the frontend; it adds nothing else.
func TestSystemdRunArgsBindTheIdleSocket(t *testing.T) {
	l := testLaunch()
	l.IdleSocket = "/run/user/1001/clown/idle/k.sock"
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if want := "--property=BindPaths=/run/user/1001/clown/idle/k.sock"; !slices.Contains(args, want) {
		t.Errorf("missing %q in %v", want, args)
	}
	base, err := testLaunch().SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != len(base)+1 {
		t.Errorf("an idle socket must add exactly its bind: %d args vs %d", len(args), len(base))
	}
}

// spinclass#419: the frontend's restart control socket lives beside the idle
// socket in $XDG_RUNTIME_DIR, but only the idle socket is bound in. The unit's
// /run/user is a tmpfs (ProtectHome=tmpfs), so nothing else there, the
// control socket included, is reachable from inside.
func TestTheControlSocketIsNotReachableFromTheUnit(t *testing.T) {
	l := testLaunch()
	l.IdleSocket = "/run/user/1001/clown/idle/k.sock"
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--property=ProtectHome=tmpfs") {
		t.Fatalf("ProtectHome=tmpfs missing: %v", args)
	}
	for _, a := range args {
		if strings.Contains(a, "/clown/control") || a == "--property=BindPaths=/run/user/1001" || a == "--property=BindPaths=/run/user/1001/clown" {
			t.Errorf("the control socket or its parents are bound in: %q", a)
		}
	}
}

func TestNoIdleSocketMeansNoIdleBind(t *testing.T) {
	args, err := testLaunch().SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.Contains(a, "/idle/") {
			t.Errorf("unexpected idle wiring %q", a)
		}
	}
}

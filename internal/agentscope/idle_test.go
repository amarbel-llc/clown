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
	if bound := bindsCovering(args, "/run/user/1001/clown/control/k.sock"); len(bound) > 0 {
		t.Errorf("the control socket or a parent of it is bound in: %q", bound)
	}
}

// clown#302: the permission counters' state dir ($XDG_RUNTIME_DIR/clown/
// permstat, permstat.Dir) is not bound into the unit, so under
// ProtectHome=tmpfs the hook cannot create it there and only `prompted` is
// counted inside --agent-scope. This pins the unit's systemd-run arguments
// (not checked live); it changes when the state dir is moved somewhere the
// unit can write.
func TestThePermstatDirIsNotBoundIntoTheUnit(t *testing.T) {
	l := testLaunch()
	l.IdleSocket = "/run/user/1001/clown/idle/k.sock"
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--property=ProtectHome=tmpfs") {
		t.Fatalf("ProtectHome=tmpfs missing: %v", args)
	}
	if bound := bindsCovering(args, "/run/user/1001/clown/permstat/0123456789abcdef.json"); len(bound) > 0 {
		t.Errorf("the permstat state dir or a parent of it is bound in: %q", bound)
	}
}

// bindsCovering returns the BindPaths=/BindReadOnlyPaths= properties in args
// whose source is path or a directory above it.
func bindsCovering(args []string, path string) []string {
	var out []string
	for _, a := range args {
		v, ok := strings.CutPrefix(a, "--property=BindPaths=")
		if !ok {
			v, ok = strings.CutPrefix(a, "--property=BindReadOnlyPaths=")
		}
		if !ok {
			continue
		}
		src, _, _ := strings.Cut(strings.TrimPrefix(v, "-"), ":")
		src = strings.TrimSuffix(src, "/")
		if src == path || strings.HasPrefix(path, src+"/") || src == "" {
			out = append(out, a)
		}
	}
	return out
}

// The check itself catches a bind of the runtime dir, a parent, or the
// control dir, rw or ro, with or without the optional "-".
func TestBindsCoveringCatchesParents(t *testing.T) {
	for _, a := range []string{
		"--property=BindPaths=/run/user/1001",
		"--property=BindReadOnlyPaths=-/run/user/1001/clown/",
		"--property=BindPaths=/run/user/1001/clown/control:/x",
	} {
		if len(bindsCovering([]string{a}, "/run/user/1001/clown/control/k.sock")) != 1 {
			t.Errorf("%q not caught", a)
		}
	}
	if got := bindsCovering([]string{"--property=BindPaths=/run/user/1001/clown/idle/k.sock"}, "/run/user/1001/clown/control/k.sock"); len(got) != 0 {
		t.Errorf("a sibling socket was flagged: %q", got)
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

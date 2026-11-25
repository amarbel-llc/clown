package agentscope

import (
	"slices"
	"strings"
	"testing"
)

const testBridgeDir = "/run/user/1001/clown/launch/key-1/bridge"

// FDR 0048, the bridge contract's MUST: the bridge directory is bound into the
// unit READ-ONLY. The unit can connect to bridge.sock but never create, replace
// or unlink anything there, so it cannot put its own socket in the server's
// place. No writable bind covers it, and no ReadWritePaths= either.
func TestTheBridgeDirIsBoundReadOnlyAndNeverWritable(t *testing.T) {
	l := testLaunch()
	l.BridgeDir = testBridgeDir
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--property=BindReadOnlyPaths="+testBridgeDir) {
		t.Fatalf("the bridge dir is not bound read-only: %v", args)
	}
	for _, a := range bindsCovering(args, testBridgeDir+"/bridge.sock") {
		if a != "--property=BindReadOnlyPaths="+testBridgeDir {
			t.Errorf("a bind other than the read-only one covers the bridge socket: %q", a)
		}
	}
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--property=ReadWritePaths="); ok {
			if v = strings.TrimSuffix(v, "/"); v == testBridgeDir || strings.HasPrefix(testBridgeDir, v+"/") || v == "" {
				t.Errorf("a writable path covers the bridge dir: %q", a)
			}
		}
	}
}

// WritableCovering is what the launch uses to refuse a bridge whose directory a
// writable mount would cover; it must see a parent, the dir itself and a
// relocated destination, and must not flag a sibling.
func TestWritableCoveringSeesParentsAndNotSiblings(t *testing.T) {
	l := testLaunch()
	l.BridgeDir = testBridgeDir
	if got := l.WritableCovering(testBridgeDir + "/bridge.sock"); len(got) != 0 {
		t.Errorf("a stock launch has writable mounts over the bridge dir: %q", got)
	}
	for name, spec := range map[string]string{
		"runtime dir":      "/run/user/1001",
		"clown dir":        "/run/user/1001/clown/",
		"the launch dir":   "/run/user/1001/clown/launch/key-1",
		"the bridge dir":   testBridgeDir,
		"optional":         "-/run/user/1001",
		"relocated source": "/elsewhere:/run/user/1001/clown/launch",
	} {
		l := testLaunch()
		l.BridgeDir = testBridgeDir
		l.Spec.BindsRW = append(l.Spec.BindsRW, spec)
		if got := l.WritableCovering(testBridgeDir + "/bridge.sock"); len(got) == 0 {
			t.Errorf("%s (%q) was not seen as covering the bridge dir", name, spec)
		}
	}
	l.Spec.BindsRW = append(l.Spec.BindsRW, "/run/user/1001/clown/launch/key-1/bridge.cgroup", "/run/user/1001/clown/launch/other")
	if got := l.WritableCovering(testBridgeDir + "/bridge.sock"); len(got) != 0 {
		t.Errorf("a sibling was flagged: %q", got)
	}
}

// The file the serve reads the unit's cgroup from sits beside the bridge dir:
// nothing the unit has bound reaches it, read-only or not.
func TestTheCgroupFileIsNotReachableFromTheUnit(t *testing.T) {
	l := testLaunch()
	l.BridgeDir = testBridgeDir
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if got := bindsCovering(args, "/run/user/1001/clown/launch/key-1/bridge.cgroup"); len(got) != 0 {
		t.Errorf("the cgroup file or a parent of it is bound in: %q", got)
	}
}

// Off by default: no bridge dir means no bridge wiring and no capability.
func TestNoBridgeDirMeansNoBridgeWiring(t *testing.T) {
	l := testLaunch()
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.Contains(a, "/bridge") {
			t.Errorf("unexpected bridge wiring %q", a)
		}
	}
	if slices.Contains(l.resultCapabilities(), "bridge-v1") {
		t.Error("bridge-v1 advertised with no bridge")
	}
	l.BridgeDir = testBridgeDir
	if !slices.Contains(l.resultCapabilities(), "bridge-v1") {
		t.Error("bridge-v1 not advertised with a bridge")
	}
	if slices.Contains(Capabilities, "bridge-v1") {
		t.Error("the shared Capabilities slice was mutated")
	}
}

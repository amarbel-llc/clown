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

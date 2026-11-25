package agentscope

import (
	"slices"
	"strings"
	"testing"
)

// The in-unit shim is told the bridge socket (it holds the control stream) and
// the provider's environment names the same socket, at the same path inside as
// outside. With no bridge, neither appears, and a CLOWN_BRIDGE_SOCKET in the
// frontend's own environment is not passed in.
func TestTheUnitIsToldTheBridgeSocketOnlyWithABridge(t *testing.T) {
	l := testLaunch()
	l.BridgeDir = testBridgeDir
	l.FrontendEnv = append(l.FrontendEnv, "CLOWN_BRIDGE_SOCKET=/elsewhere/bridge.sock")
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	sock := testBridgeDir + "/bridge.sock"
	i := slices.Index(args, "--bridge")
	if i < 0 || i+1 >= len(args) || args[i+1] != sock {
		t.Fatalf("--bridge %s missing from %v", sock, args)
	}
	// After systemd-run's own "--" (it is a flag of the shim, not of systemd-run)
	// and before the provider's "--".
	first := slices.Index(args, "--")
	next := slices.Index(args[first+1:], "--")
	if first < 0 || next < 0 || i < first || i > first+1+next {
		t.Errorf("--bridge is not among the shim's own flags: %v", args)
	}
	if !slices.Contains(args, "--setenv=CLOWN_BRIDGE_SOCKET="+sock) {
		t.Errorf("the provider's environment does not name the bridge socket: %v", args)
	}

	off := testLaunch()
	off.FrontendEnv = append(off.FrontendEnv, "CLOWN_BRIDGE_SOCKET=/elsewhere/bridge.sock")
	args, err = off.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "--bridge") {
		t.Errorf("--bridge without a bridge: %v", args)
	}
	for _, a := range args {
		if strings.Contains(a, "CLOWN_BRIDGE_SOCKET") {
			t.Errorf("a bridge socket reached a unit with no bridge: %q", a)
		}
	}
}

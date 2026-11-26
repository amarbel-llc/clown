package agentscope

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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

// Round 2, M3: the result says what became of the bridge: nothing when it was
// never asked for, "recorded", or "off: <reason>" when the operator asked and the
// frontend knows why it is missing. Red without the field.
func TestTheResultSaysWhyTheBridgeIsOff(t *testing.T) {
	l := testLaunch()
	if got := l.baseResult().Bridge; got != "" {
		t.Errorf("never asked for: bridge = %q, want empty", got)
	}
	l.BridgeStatus = &BridgeStatus{}
	if got := l.baseResult().Bridge; !strings.HasPrefix(got, "off: ") {
		t.Errorf("asked for, no dir, no reason: bridge = %q, want an off line", got)
	}
	l.BridgeStatus.Off("the serve created no bridge socket in 10s")
	l.BridgeStatus.Off("a second reason is ignored")
	if got := l.baseResult().Bridge; got != "off: the serve created no bridge socket in 10s" {
		t.Errorf("bridge = %q, want the first reason", got)
	}
	ok := testLaunch()
	ok.BridgeDir = testBridgeDir
	ok.BridgeStatus = &BridgeStatus{}
	ok.BridgeReady = &atomic.Bool{}
	if got := ok.baseResult().Bridge; !strings.HasPrefix(got, "off: ") {
		t.Errorf("dir bound but cgroup not recorded: bridge = %q, want an off line", got)
	}
	ok.BridgeReady.Store(true)
	if got := ok.baseResult().Bridge; got != "recorded" {
		t.Errorf("bridge = %q, want recorded (never \"on\": nothing here knows the listener admits the unit)", got)
	}
	path := filepath.Join(t.TempDir(), "r", "result.json")
	if err := WriteResult(path, ok.baseResult()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"bridge": "recorded"`) {
		t.Errorf("the result file does not carry the field: %s", raw)
	}
}

// Round 1, A-M2: a writable bind of the cgroup file itself is a covering mount
// for THAT path (the trust anchor), and a bind reaching the runtime dir through
// a symlink is seen. Red with the lexical-only rule.
func TestWritableCoveringCoversTheCgroupFileAndSymlinks(t *testing.T) {
	const cgFile = "/run/user/1001/clown/launch/key-1/bridge.cgroup"
	l := testLaunch()
	l.BridgeDir = testBridgeDir
	l.Spec.BindsRW = append(l.Spec.BindsRW, cgFile)
	if got := l.WritableCovering(cgFile); len(got) == 0 {
		t.Error("a writable bind of bridge.cgroup itself was not flagged for bridge.cgroup")
	}
	if got := l.WritableCovering(testBridgeDir + "/bridge.sock"); len(got) != 0 {
		t.Errorf("a bind of the cgroup file was flagged for the socket: %q", got)
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "rt")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	l = testLaunch()
	l.Spec.BindsRW = append(l.Spec.BindsRW, link) // a link to the real directory
	if got := l.WritableCovering(filepath.Join(real, "clown", "launch", "k", "bridge", "bridge.sock")); len(got) == 0 {
		t.Error("a bind through a symlink to the real directory was not flagged")
	}
}

// Round 1, A-L3: the serve's variables never reach the unit, not from the
// frontend's environment and not from a spec's env. (The switch is the spec's
// `bridge` field now; there is no environment switch to scrub.)
func TestTheBridgeVariablesNeverReachTheUnit(t *testing.T) {
	l := testLaunch()
	l.FrontendEnv = append(l.FrontendEnv, "CLOWN_BRIDGE_DIR=/x", "CLOWN_BRIDGE_UNIT=u.service", "CLOWN_BRIDGE_CGROUP_FILE=/y")
	l.Spec.Env = map[string]string{"CLOWN_BRIDGE_DIR": "/spec", "CLOWN_BRIDGE_CGROUP_FILE": "/spec"}
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		for _, k := range []string{"CLOWN_BRIDGE_DIR", "CLOWN_BRIDGE_UNIT", "CLOWN_BRIDGE_CGROUP_FILE"} {
			if strings.Contains(a, "--setenv="+k+"=") {
				t.Errorf("%s reached the unit: %q", k, a)
			}
		}
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

// Round 1, A-M1: a spec whose worktree or binds_rw covers or lies inside the
// launch root is refused (other sessions' cgroup files and sockets live there),
// through a symlink as well. Red without the Validate rule.
func TestValidateRefusesWritableLaunchDirs(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("XDG_STATE_HOME", "")
	launch := filepath.Join(rt, "clown", "launch")
	if err := os.MkdirAll(filepath.Join(launch, "k1"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "rtlink")
	if err := os.Symlink(rt, link); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{rt, filepath.Join(rt, "clown"), launch, filepath.Join(launch, "k1"), filepath.Join(launch, "k1", "bridge"), link, filepath.Join(link, "clown", "launch", "k1")} {
		s := validSpec()
		s.BindsRW = []string{bad}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "launch directories") {
			t.Errorf("binds_rw %q: err %v, want the launch-directory refusal", bad, err)
		}
	}
	// Round 2, M1: a `sockets` entry is a read-write bind of whatever file type it
	// names, and a read-only bind of the root makes every session's socket
	// connectable: both are refused too.
	for _, bad := range []string{launch, filepath.Join(launch, "k1", "bridge.cgroup"), filepath.Join(launch, "k1", "bridge", "bridge.sock"), filepath.Join(link, "clown", "launch")} {
		s := validSpec()
		s.Sockets = []Socket{{Path: bad, Purpose: "x"}}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "launch directories") {
			t.Errorf("sockets %q: err %v, want the launch-directory refusal", bad, err)
		}
		s = validSpec()
		s.BindsRO = []string{bad}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "launch directories") {
			t.Errorf("binds_ro %q: err %v, want the launch-directory refusal", bad, err)
		}
	}
	s := validSpec()
	s.Worktree = filepath.Join(launch, "k1", "wt")
	if err := s.Validate(); err == nil {
		t.Error("a worktree inside the launch root was accepted")
	}
	s = validSpec()
	s.BindsRW = []string{filepath.Join(rt, "other")}
	if err := s.Validate(); err != nil {
		t.Errorf("an unrelated runtime path: %v", err)
	}
}

// Round 1, H1: the cgroup is handed over when the main process settles, BEFORE
// the shim has started the provider (the in-unit bridge dials from the shim),
// exactly once, with the unit's own ControlGroup. Red without waitActiveNotify:
// the callback then never fires before the provider is seen.
func TestTheCgroupIsHandedOverBeforeTheProviderStarts(t *testing.T) {
	const unit = "u-1.service"
	cg := "/user.slice/user-1001.slice/user@1001.service/clown.slice/" + Slice + "/" + unit
	root := t.TempDir()
	fakeProc(t, root, 100, cg, "100 1")
	setCmdline(t, root, 100, "/nix/store/x-clown/bin/clown", ShimVerb, "--relay", "a=b", "--", "claude")
	var got []string
	providerSeenAtCallback := false
	polls := 0
	tools := Tools{procRoot: root, show: func(string) (Status, error) {
		polls++
		if polls == 4 {
			setCmdline(t, root, 100, "claude", "-p", "hi")
		}
		return Status{ActiveState: "active", ControlGroup: cg, MainPID: 100}, nil
	}}
	_, err := tools.waitActiveNotify(context.Background(), unit, func(c string) {
		got = append(got, c)
		providerSeenAtCallback = tools.providerStarted(100)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != cg {
		t.Errorf("callback calls = %q, want exactly [%q]", got, cg)
	}
	if providerSeenAtCallback {
		t.Error("the cgroup was handed over only after the provider had started")
	}
}

// Spec.Bridge is "", "offered" or "required"; anything else is refused (a mode
// this build does not know may be one it was meant to enforce).
func TestSpecBridgeModeIsValidated(t *testing.T) {
	for _, m := range []string{"", BridgeOffered, BridgeRequired} {
		s := validSpec()
		s.Bridge = m
		if err := s.Validate(); err != nil {
			t.Errorf("bridge %q refused: %v", m, err)
		}
	}
	s := validSpec()
	s.Bridge = "mandatory"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "bridge") {
		t.Errorf("an unknown mode: err = %v", err)
	}
	if validSpec().BridgeIsRequired() {
		t.Error("an unset bridge field means required; it means OFF (the one switch is off by default)")
	}
}

// A required bridge whose unit's cgroup cannot be recorded refuses the launch
// and kills the unit; an offered one (the default) goes on. The fake onCgroup
// leaves BridgeReady false, as a failed write does. Red without the wrapper in
// Run: the launch then succeeds with no bridge.
func TestARequiredBridgeThatCannotBeSetUpRefusesTheLaunch(t *testing.T) {
	for _, mode := range []string{BridgeRequired, "", BridgeOffered} {
		t.Run("mode="+mode, func(t *testing.T) {
			l := testLaunch()
			l.Spec.Bridge = mode
			l.Spec.ResultPath = filepath.Join(t.TempDir(), "result.json")
			l.BridgeDir = testBridgeDir
			l.BridgeReady = &atomic.Bool{}
			l.BridgeStatus = &BridgeStatus{}
			l.BridgeStatus.Off("could not record the unit's cgroup")
			l.OnCgroup = func(string) {} // the write "failed": BridgeReady stays false
			cg := "/user.slice/user-1001.slice/user@1001.service/clown.slice/" + Slice + "/" + l.Unit
			root := t.TempDir()
			fakeProc(t, root, 100, cg, "100 1")
			sdrun := filepath.Join(t.TempDir(), "systemd-run")
			if err := os.WriteFile(sdrun, []byte("#!/bin/sh\nsleep 1\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			systemctl, calls := fakeSystemctl(t)
			tools := Tools{SystemdRun: sdrun, Systemctl: systemctl, procRoot: root, show: func(string) (Status, error) {
				return Status{ActiveState: "active", ControlGroup: cg, MainPID: 100}, nil
			}}
			_, err := Run(l, tools, nil)
			killed := false
			if b, _ := os.ReadFile(calls); strings.Contains(string(b), "--user kill --signal=SIGKILL "+l.Unit) {
				killed = true
			}
			if mode == BridgeRequired {
				if err == nil || !strings.Contains(err.Error(), "the bridge is required") || !strings.Contains(err.Error(), "could not record the unit's cgroup") {
					t.Errorf("required: err = %v, want the refusal with the reason", err)
				}
				if !killed {
					t.Error("required: the unit was not killed")
				}
				return
			}
			if err != nil && strings.Contains(err.Error(), "the bridge is required") {
				t.Errorf("%q: the launch was refused: %v", mode, err)
			}
			if killed {
				t.Errorf("%q: the unit was killed", mode)
			}
		})
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
	// Round 1, A-L2: the directory being bound is not enough; the cgroup file
	// must have been written (the launcher sets BridgeReady then).
	if slices.Contains(l.resultCapabilities(), "bridge-v1") {
		t.Error("bridge-v1 advertised for a bound dir whose cgroup file was never written")
	}
	l.BridgeReady = &atomic.Bool{}
	if slices.Contains(l.resultCapabilities(), "bridge-v1") {
		t.Error("bridge-v1 advertised before the cgroup write")
	}
	l.BridgeReady.Store(true)
	if !slices.Contains(l.resultCapabilities(), "bridge-v1") {
		t.Error("bridge-v1 not advertised with a bridge that is ready")
	}
	if slices.Contains(Capabilities, "bridge-v1") {
		t.Error("the shared Capabilities slice was mutated")
	}
}

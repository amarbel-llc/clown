package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/permstat"
)

// clown#302: the permission counters' state dir (permstat.Dir) lies under a
// read-write bind of the unit, so all three counters work inside
// --agent-scope. The unit sets XDG_STATE_HOME to the frontend's value
// (agentscope.OperatorXDG), so the path is the same inside and out.
//
// Pinned exactly: the bind is $XDG_STATE_HOME/clown/hook-toolcost itself (not
// a wider prefix such as all of XDG_STATE_HOME), permstat.Dir() is directly
// under it, and it is not under the runtime dir (a read-only tmpfs in a
// unit), so it fails if the state moves back there or out of the bind.
func TestStageAgentHomeBindsThePermstatDir(t *testing.T) {
	runtime, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", state)
	p, err := stageAgentHome(agentscope.Spec{OperatorHome: t.TempDir()}, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bind := filepath.Join(state, "clown", "hook-toolcost")
	dir := permstat.Dir()
	if filepath.Dir(dir) != bind {
		t.Fatalf("permstat.Dir() = %s, want a directory directly under %s", dir, bind)
	}
	if strings.HasPrefix(dir, runtime+"/") {
		t.Fatalf("permstat.Dir() %s is under XDG_RUNTIME_DIR", dir)
	}
	if !slices.Contains(p.rw, bind) {
		t.Fatalf("read-write binds %v, want exactly %s", p.rw, bind)
	}
	for _, b := range p.rw {
		src, _, _ := strings.Cut(b, ":")
		if src != bind && strings.HasPrefix(bind, strings.TrimSuffix(src, "/")+"/") {
			t.Errorf("a wider read-write bind %s covers %s", src, bind)
		}
	}
	if fi, err := os.Stat(bind); err != nil || !fi.IsDir() {
		t.Fatalf("the bind source %s is not a directory: %v", bind, err)
	}
}

// TestStageAgentHomeBindsTheOperatorCredentialReadOnly: a worker can read the
// operator's claude credential but must not overwrite or truncate it.
func TestStageAgentHomeBindsTheOperatorCredentialReadOnly(t *testing.T) {
	opHome, agentHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	creds := filepath.Join(opHome, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := stageAgentHome(agentscope.Spec{OperatorHome: opHome}, agentHome, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := creds + ":" + filepath.Join(agentHome, ".claude", ".credentials.json")
	if !slices.Contains(p.ro, want) {
		t.Errorf("read-only binds %v, want %q", p.ro, want)
	}
	for _, b := range p.rw {
		if strings.HasPrefix(b, creds) {
			t.Errorf("credential bound read-write: %q", b)
		}
	}
}

// TestStageAgentHomeBindsNoJobPlatformState pins the confined unit's job
// platform surface: the session's own troupe password file, read-only, and
// nothing else of troupe's or ringmaster's (every session's passwords,
// sockets, cursors, job journals and presence). The monitors that used those
// run in the frontend (moveMonitorsToFrontend).
func TestStageAgentHomeBindsNoJobPlatformState(t *testing.T) {
	opHome, runtime, state := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", state)
	pw := filepath.Join(runtime, "troupe", "accounts", "sess-1.pass")
	t.Setenv("TROUPE_XMPP_PASSWORD_FILE", pw)
	t.Setenv("TROUPE_XMPP_USER", "sess-1")
	t.Setenv("TROUPE_XMPP_DOMAIN", "xmpp.test")

	p, err := stageAgentHome(agentscope.Spec{OperatorHome: opHome}, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.ro, pw) || slices.Contains(p.rw, pw) {
		t.Errorf("binds ro=%v rw=%v, want the own password file read-only", p.ro, p.rw)
	}
	platform := []string{
		filepath.Join(runtime, "troupe"), filepath.Join(state, "troupe"),
		filepath.Join(runtime, "ringmaster"), filepath.Join(state, "ringmaster"),
	}
	for _, b := range append(slices.Clone(p.rw), p.ro...) {
		src, _, _ := strings.Cut(b, ":")
		for _, dir := range platform {
			if (src == dir || strings.HasPrefix(src, dir+"/")) && src != pw {
				t.Errorf("bind %q exposes job-platform state", b)
			}
		}
	}

	// The launch artifact: the systemd-run argv the unit is started with.
	wt := t.TempDir()
	mon := filepath.Join(runtime, "clown", "monitors", "sess-key", "ringmaster-monitor.sock")
	args, err := agentscope.Launch{
		Unit: "u-1.service", WorkDir: wt, Spec: agentscope.Spec{Worktree: wt},
		Argv: []string{"claude"}, EgressSocket: filepath.Join(runtime, "egress.sock"),
		PlumbingRW: p.rw, PlumbingRO: p.ro, MonitorSockets: []string{mon},
	}.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--property=BindPaths="+mon) {
		t.Errorf("unit args %v, want the monitor socket bound", args)
	}
	for _, a := range args {
		if a == "--property=BindReadOnlyPaths=-"+pw || a == "--property=BindPaths="+mon {
			continue
		}
		if strings.Contains(a, "/troupe") || strings.Contains(a, "/ringmaster") {
			t.Errorf("unit argument %q binds job-platform state", a)
		}
	}
}

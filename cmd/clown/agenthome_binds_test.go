package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
)

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

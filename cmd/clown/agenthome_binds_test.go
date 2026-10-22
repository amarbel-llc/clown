package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// TestStageAgentHomeBindsNoTroupeDirectory pins the confined unit's troupe
// surface: the session's own password file and read cursors, never the
// accounts/ directory (every session's password), the runtime socket dir or
// other accounts' cursors.
func TestStageAgentHomeBindsNoTroupeDirectory(t *testing.T) {
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
	cursors := filepath.Join(state, "troupe", "cursors", "sess-1@xmpp.test")
	if !slices.Contains(p.ro, pw) || !slices.Contains(p.ro, cursors) {
		t.Errorf("ro binds %v, want the own password file and %s", p.ro, cursors)
	}
	for _, b := range append(slices.Clone(p.rw), p.ro...) {
		src, _, _ := strings.Cut(b, ":")
		for _, dir := range []string{filepath.Join(runtime, "troupe"), filepath.Join(state, "troupe")} {
			if (src == dir || strings.HasPrefix(src, dir+"/")) && src != pw && src != cursors {
				t.Errorf("bind %q exposes troupe state beyond the session's own", b)
			}
		}
	}
	if slices.Contains(p.rw, pw) || slices.Contains(p.rw, cursors) {
		t.Errorf("the own troupe files must be read-only: rw = %v", p.rw)
	}

	// The launch artifact: the systemd-run argv the unit is started with.
	wt := t.TempDir()
	args, err := agentscope.Launch{
		Unit: "u-1.service", WorkDir: wt, Spec: agentscope.Spec{Worktree: wt},
		Argv: []string{"claude"}, EgressSocket: filepath.Join(runtime, "egress.sock"),
		PlumbingRW: p.rw, PlumbingRO: p.ro,
	}.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if !strings.Contains(a, "/troupe") {
			continue
		}
		if want := "--property=BindReadOnlyPaths=-"; a != want+pw && a != want+cursors {
			t.Errorf("unit argument %q binds troupe state beyond the session's own", a)
		}
	}
}

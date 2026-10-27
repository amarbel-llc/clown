package agentscope

import (
	"strings"
	"testing"
)

func unitEnv(l Launch) map[string]string {
	set, _ := l.environment()
	env := map[string]string{}
	for _, kv := range set {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}

// `just` makes its recipe tempdir under $XDG_RUNTIME_DIR/just unless
// JUST_TEMPDIR says otherwise, and in a unit /run/user is a read-only tmpfs, so
// every run_recipe failed with EROFS (confined probe on flic, 2026-10-27). The unit
// points just at the writable temp dir it already gives TMPDIR.
func TestEnvironmentPointsJustAtTheWritableTempDir(t *testing.T) {
	const tmp = "/home/op/repos/x/.worktrees/w/.tmp"

	// TMPDIR outside the worktree is redirected to the worktree's .tmp; so is JUST_TEMPDIR.
	env := unitEnv(testLaunch())
	if env["JUST_TEMPDIR"] != tmp || env["TMPDIR"] != tmp {
		t.Errorf("JUST_TEMPDIR = %q, TMPDIR = %q, want both %q", env["JUST_TEMPDIR"], env["TMPDIR"], tmp)
	}

	// A TMPDIR already inside the worktree is the one used.
	l := testLaunch()
	l.FrontendEnv = append(l.FrontendEnv, "TMPDIR="+tmp+"/sub")
	if got := unitEnv(l)["JUST_TEMPDIR"]; got != tmp+"/sub" {
		t.Errorf("JUST_TEMPDIR = %q, want it to follow the unit's TMPDIR %q", got, tmp+"/sub")
	}

	// No TMPDIR at all: the worktree's .tmp.
	l = testLaunch()
	l.FrontendEnv = []string{"PATH=/bin"}
	if got := unitEnv(l)["JUST_TEMPDIR"]; got != tmp {
		t.Errorf("without TMPDIR JUST_TEMPDIR = %q, want %q", got, tmp)
	}

	// XDG_RUNTIME_DIR still passes (job-platform tools find paths from it).
	l = testLaunch()
	l.FrontendEnv = append(l.FrontendEnv, "XDG_RUNTIME_DIR=/run/user/1001")
	if got := unitEnv(l)["XDG_RUNTIME_DIR"]; got != "/run/user/1001" {
		t.Errorf("XDG_RUNTIME_DIR = %q, want it passed through unchanged", got)
	}

	// A spec can override it.
	l = testLaunch()
	l.Spec.Env = map[string]string{"JUST_TEMPDIR": "/home/op/repos/x/.worktrees/w/other"}
	if got := unitEnv(l)["JUST_TEMPDIR"]; got != "/home/op/repos/x/.worktrees/w/other" {
		t.Errorf("spec JUST_TEMPDIR = %q, want the spec's", got)
	}
}

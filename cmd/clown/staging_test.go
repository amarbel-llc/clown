package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/buildcfg"
	"code.linenisgreat.com/clown/internal/staging"
)

// testStagingRoot returns a launch staging root scoped to the test, closed on
// cleanup. It replaces the per-test `os.RemoveAll(dir)` the synth-dir tests
// used to carry: the root owns those directories now.
func testStagingRoot(t *testing.T) *staging.Root {
	t.Helper()
	r, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatalf("staging.New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// The two synthesized built-in plugin dirs must each get their OWN directory
// under the launch root.
//
// This is the one property in the staging migration that no other lane can
// see. The launch-plan goldens observe only what reaches claude's argv, and
// these two dirs never do: they are INPUTS to CompilePluginDir, and what argv
// carries is the compiled clown-plugin-compile-* dir derived from each. So a
// migration that handed both synth functions one shared directory would leave
// every golden byte-identical and pass green — while the juggler plugin's
// clown.json and manifest silently overwrote the job-monitor's, costing the
// session its job-wakeup monitor and its MCP tool servers with no diagnostic.
func TestSynthPluginDirs_GetDistinctDirsUnderOneRoot(t *testing.T) {
	// Both synth functions no-op unless their binary path is burned in, which
	// only the nix derivation does; without this the test would compare two
	// empty strings and pass vacuously.
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	t.Setenv("CLOWN_DISABLE_JUGGLER_MCP", "")
	origRM, origJuggler := buildcfg.RingmasterPath, buildcfg.JugglerCliPath
	buildcfg.RingmasterPath = "/nix/store/x/bin/ringmaster"
	buildcfg.JugglerCliPath = "/nix/store/x/bin/juggler"
	t.Cleanup(func() {
		buildcfg.RingmasterPath, buildcfg.JugglerCliPath = origRM, origJuggler
	})

	root := testStagingRoot(t)

	monitorDir, err := synthJobMonitorPluginDir(root, "sess-key")
	if err != nil {
		t.Fatalf("synthJobMonitorPluginDir: %v", err)
	}
	jugglerDir, err := synthJugglerPluginDir(root)
	if err != nil {
		t.Fatalf("synthJugglerPluginDir: %v", err)
	}

	if monitorDir == "" || jugglerDir == "" {
		t.Fatalf("a synth dir was skipped (monitor=%q juggler=%q); the distinctness check below would be vacuous", monitorDir, jugglerDir)
	}
	if monitorDir == jugglerDir {
		t.Errorf("job-monitor and juggler plugins share one dir %q; each overwrites the other's manifest", monitorDir)
	}
	for name, dir := range map[string]string{"job-monitor": monitorDir, "juggler": jugglerDir} {
		if !strings.HasPrefix(dir, root.Path()) {
			t.Errorf("%s dir %q is not under the launch root %q", name, dir, root.Path())
		}
	}

	// Each must still carry its own manifest naming its own plugin — the
	// observable a shared directory would destroy, and the reason distinctness
	// matters at all.
	for _, tc := range []struct{ dir, want string }{
		{monitorDir, "clown-builtin-jobs"},
		{jugglerDir, "clown-builtin-juggler"},
	} {
		b, err := os.ReadFile(filepath.Join(tc.dir, ".claude-plugin", "plugin.json"))
		if err != nil {
			t.Fatalf("reading manifest in %s: %v", tc.dir, err)
		}
		var m struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("manifest in %s is not valid JSON: %v\n%s", tc.dir, err, b)
		}
		if m.Name != tc.want {
			t.Errorf("manifest in %s names plugin %q, want %q", tc.dir, m.Name, tc.want)
		}
	}
}

// The clownbox sandbox shadows $HOME and gives the container a fresh /tmp, so a
// staging root under the ambient $TMPDIR is INVISIBLE from inside it. Before the
// staging migration runClownbox bought that visibility by pointing $TMPDIR at
// <repo>/.tmp for the duration of arg-building; the root has to land in the same
// place, or clown's prompt-append file silently vanishes inside the sandbox and
// the system prompt goes quietly missing rather than erroring.
//
// That setenv is now deleted, so this arm is the ONLY thing placing clownbox's
// artifacts inside the bind-mount and this test is the only thing guarding it.
//
// Pinned as a pure function because the failure is invisible from the outside:
// nothing errors, claude just runs without clown's prompt.
func TestStagingBaseFor_ClownboxLandsInRepo(t *testing.T) {
	// Hermetic against an operator (or a CI image) that exports the rollback
	// lever: an inherited CLOWN_STAGING_ROOT=tmpdir would otherwise turn this
	// guard red for a reason that has nothing to do with the policy it pins.
	t.Setenv(stagingRootEnv, "")

	cwd := "/repo/root"
	var warn bytes.Buffer
	got := stagingBaseFor(parsedFlags{provider: "clownbox"}, cwd, &warn)
	if want := filepath.Join(cwd, ".tmp"); got != want {
		t.Errorf("stagingBaseFor(clownbox) = %q, want %q", got, want)
	}
	// An empty value is an unset value — `export CLOWN_STAGING_ROOT=` and
	// `unset CLOWN_STAGING_ROOT` are the same intent, so neither may be
	// reported as a typo.
	if warn.Len() != 0 {
		t.Errorf("unset %s warned: %q", stagingRootEnv, warn.String())
	}
}

// Every other provider keeps today's placement: an empty base means
// staging.New falls through to os.MkdirTemp's $TMPDIR default. Asserting the
// empty string rather than a resolved path is deliberate — resolving $TMPDIR
// here would duplicate os.MkdirTemp's own logic and pin the wrong thing.
func TestStagingBaseFor_OtherProvidersUseTmpdir(t *testing.T) {
	t.Setenv(stagingRootEnv, "")

	for _, provider := range []string{"claude", "codex", "opencode", "crush", "openrouter", "juggler"} {
		var warn bytes.Buffer
		if got := stagingBaseFor(parsedFlags{provider: provider}, "/repo/root", &warn); got != "" {
			t.Errorf("stagingBaseFor(%s) = %q, want \"\" ($TMPDIR default)", provider, got)
		}
	}
}

// CLOWN_STAGING_ROOT=tmpdir is the rollback lever for the one decision in the
// staging migration that can go wrong in the field: WHERE the root lands. It
// cannot un-invent the root — every artifact is under it by construction now —
// so what it actually does is pin the root's base back to the default, beating
// whatever policy stagingBaseFor would otherwise apply.
//
// clownbox is the case worth pinning because it is the only provider with a
// non-default arm today: if that arm is what is causing trouble in the field,
// this is how an operator rules it out without waiting for a revert.
func TestStagingBaseFor_EnvOverrideForcesTmpdir(t *testing.T) {
	t.Setenv(stagingRootEnv, stagingRootTmpdir)

	for _, provider := range []string{"clownbox", "claude", "opencode", "crush"} {
		var warn bytes.Buffer
		if got := stagingBaseFor(parsedFlags{provider: provider}, "/repo/root", &warn); got != "" {
			t.Errorf("stagingBaseFor(%s) with %s=%s = %q, want \"\" ($TMPDIR default)",
				provider, stagingRootEnv, stagingRootTmpdir, got)
		}
		if warn.Len() != 0 {
			t.Errorf("the documented value warned: %q", warn.String())
		}
	}
}

// An unrecognised value must do BOTH halves of this, and the pair is the whole
// decision:
//
//   - It must not change placement. Erroring out, or guessing, would let a stale
//     export in a shell profile break every launch — a far larger blast radius
//     than the one misconfiguration it would be protecting against.
//   - It must say so on stderr. This is a rollback lever, and a silently-ignored
//     typo leaves an operator believing they have rolled back when they have
//     not, which is precisely the state in which they draw the wrong conclusion
//     about whether placement was the cause.
//
// An explicit path is deliberately NOT accepted, and is tested here as just
// another unrecognised value: this is a lever scheduled for removal after one
// clean release, and a path would make it a configuration feature (relative vs
// absolute, who creates it, what mode, how it interacts with clownbox's
// bind-mount requirement) that is much harder to withdraw.
func TestStagingBaseFor_UnrecognisedValueWarnsAndKeepsPolicy(t *testing.T) {
	for _, value := range []string{"tmpdirr", "TMPDIR", "1", "true", "/some/explicit/path"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(stagingRootEnv, value)

			cwd := "/repo/root"
			var warn bytes.Buffer
			got := stagingBaseFor(parsedFlags{provider: "clownbox"}, cwd, &warn)
			if want := filepath.Join(cwd, ".tmp"); got != want {
				t.Errorf("stagingBaseFor(clownbox) with %s=%q = %q, want %q (policy unchanged)",
					stagingRootEnv, value, got, want)
			}

			// The warning has to be actionable on its own: what was ignored,
			// and what would have worked.
			for _, want := range []string{stagingRootEnv, value, stagingRootTmpdir} {
				if !strings.Contains(warn.String(), want) {
					t.Errorf("warning %q does not mention %q", warn.String(), want)
				}
			}
		})
	}
}

package main

import (
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
	cwd := "/repo/root"
	got := stagingBaseFor(parsedFlags{provider: "clownbox"}, cwd)
	if want := filepath.Join(cwd, ".tmp"); got != want {
		t.Errorf("stagingBaseFor(clownbox) = %q, want %q", got, want)
	}
}

// Every other provider keeps today's placement: an empty base means
// staging.New falls through to os.MkdirTemp's $TMPDIR default. Asserting the
// empty string rather than a resolved path is deliberate — resolving $TMPDIR
// here would duplicate os.MkdirTemp's own logic and pin the wrong thing.
func TestStagingBaseFor_OtherProvidersUseTmpdir(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "opencode", "crush", "openrouter", "juggler"} {
		if got := stagingBaseFor(parsedFlags{provider: provider}, "/repo/root"); got != "" {
			t.Errorf("stagingBaseFor(%s) = %q, want \"\" ($TMPDIR default)", provider, got)
		}
	}
}

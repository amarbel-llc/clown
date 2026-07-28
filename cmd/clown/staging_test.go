package main

import (
	"path/filepath"
	"testing"

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

// The clownbox sandbox shadows $HOME and gives the container a fresh /tmp, so a
// staging root under the ambient $TMPDIR is INVISIBLE from inside it. Before the
// staging migration runClownbox bought that visibility by pointing $TMPDIR at
// <repo>/.tmp for the duration of arg-building; the root has to land in the same
// place, or clown's prompt-append file silently vanishes inside the sandbox and
// the system prompt goes quietly missing rather than erroring.
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

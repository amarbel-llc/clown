package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/promptwalk"
)

// The prompt sources, at their module-relative paths: the godyn lane places
// them in cmd/clown's run tree (flake.nix testFiles), as `go test` would see
// them in a checkout. Missing is a failure, not a skip.
const (
	systemPromptAppendD = "../../system-prompt-append.d"
	subagentsD          = "../../subagents"
)

// The builtin system-prompt-append.d fragments, assembled as clown does at
// launch (promptwalk.WalkPrompts with buildcfg.SystemPromptAppendD first).
func builtinAppendPrompt(t *testing.T) string {
	t.Helper()
	if fi, err := os.Stat(systemPromptAppendD); err != nil || !fi.IsDir() {
		t.Fatalf("%s is missing (%v); the godyn lane places it via testFiles", systemPromptAppendD, err)
	}
	home := t.TempDir()
	res, err := promptwalk.WalkPrompts(home, home, []string{systemPromptAppendD})
	if err != nil {
		t.Fatal(err)
	}
	if res.AppendFragments == "" {
		t.Fatalf("no fragments assembled from %s", systemPromptAppendD)
	}
	return res.AppendFragments
}

// subagentPrompt reads a clown subagent definition (subagents/<name>.md).
func subagentPrompt(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(subagentsD, name))
	if err != nil {
		t.Fatalf("%v; the godyn lane places %s via testFiles", err, subagentsD)
	}
	return string(data)
}

func requireAll(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s lacks %q", what, w)
		}
	}
}

// clown#18: a TODO/FIXME paired with an open issue names it as TODO(#NNN):,
// and the harness's "don't reference issues in comments" rule is scoped to
// after-the-fact references.
func TestBuiltinPromptPrefersTODOIssueRefs(t *testing.T) {
	requireAll(t, "the builtin append prompt", builtinAppendPrompt(t),
		"`TODO(#NNN): <description>`",
		"`FIXME(#NNN): <description>`",
		"`TODO(owner/repo#NNN):`",
		"PR-scoped",
	)
}

// clown#243: agents never read or write the harness's file-based memory.
func TestBuiltinPromptForbidsFileBasedMemory(t *testing.T) {
	requireAll(t, "the builtin append prompt", builtinAppendPrompt(t),
		"~/.claude/projects/<project>/memory/",
		"`MEMORY.md`",
		"MUST NOT",
		"overrides",
	)
}

// clown#120: epoch timestamps are never converted to dates by hand, in the
// main session and in the Discover subagent (which has no conversion tool).
func TestPromptsForbidHandConvertingEpochs(t *testing.T) {
	requireAll(t, "the builtin append prompt", builtinAppendPrompt(t),
		"epoch",
		"`date -d @N`",
		"date tool",
	)
	requireAll(t, "subagents/discover.md", subagentPrompt(t, "discover.md"),
		"epoch",
		"unconverted",
	)
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/promptwalk"
)

// The builtin system-prompt-append.d fragments, assembled as clown does at
// launch (promptwalk.WalkPrompts with buildcfg.SystemPromptAppendD first).
// The godyn lane hands the source directory in as
// CLOWN_TEST_SYSTEM_PROMPT_APPEND_D (flake.nix testEnv).
func builtinAppendPrompt(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("CLOWN_TEST_SYSTEM_PROMPT_APPEND_D")
	if dir == "" {
		t.Fatal("CLOWN_TEST_SYSTEM_PROMPT_APPEND_D is unset; the godyn test lane sets it to system-prompt-append.d")
	}
	home := t.TempDir()
	res, err := promptwalk.WalkPrompts(home, home, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.AppendFragments == "" {
		t.Fatalf("no fragments assembled from %s", dir)
	}
	return res.AppendFragments
}

// subagentPrompt reads a clown subagent definition (subagents/<name>.md),
// handed in as CLOWN_TEST_SUBAGENTS_D.
func subagentPrompt(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv("CLOWN_TEST_SUBAGENTS_D")
	if dir == "" {
		t.Fatal("CLOWN_TEST_SUBAGENTS_D is unset; the godyn test lane sets it to subagents")
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
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

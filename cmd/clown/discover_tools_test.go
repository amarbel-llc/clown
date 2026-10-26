package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// discoverMoxyTools are the moxy tools Discover may name, read from the
// current moxy's tool list (2026-10-26): read-only folio, rg, man and the
// read forms of grit. Claude Code refuses to spawn a subagent whose tools list
// resolves to nothing (clown#331), so a name that moxy does not serve is not
// harmless in a session where it is the only one that would have resolved; a
// session whose moxy is cut down (a confined unit, a cheap-context profile)
// resolves only the portable core below.
var discoverMoxyTools = []string{
	"folio_glob", "folio_read", "folio_ls", "rg_search",
	"man_list", "man_toc", "man_section",
	"grit_stage", "grit_branch",
}

// discoverPortableCore is what every session that has moxy at all keeps.
// (the builtin templates that keep moxy all keep these three).
var discoverPortableCore = []string{"folio_read", "folio_ls", "rg_search"}

// staleDiscoverNames were in the list or the prompt and are not in moxy (clown#331).
var staleDiscoverNames = []string{"folio_read-range", "folio_read-excluding", "man_search", "man_semantic-search", "grit_diff", "grit_git-rev-parse", "get-hubbed"}

func discoverToolList(t *testing.T) []string {
	t.Helper()
	line := regexp.MustCompile(`(?m)^tools = \[(.*)\]$`).FindStringSubmatch(subagentPrompt(t, "discover.md"))
	if line == nil {
		t.Fatal("subagents/discover.md has no `tools = [...]` line")
	}
	var tools []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(line[1], -1) {
		tools = append(tools, m[1])
	}
	return tools
}

// clown#331: Discover's tools are names moxy serves, and the portable core is
// among them, so the subagent resolves in an attended session and in a
// confined unit that keeps moxy.
func TestDiscoverToolsAreRealMoxyTools(t *testing.T) {
	tools := discoverToolList(t)
	const prefix = "mcp__plugin_moxy_moxy__"
	var names []string
	for _, tool := range tools {
		name, ok := strings.CutPrefix(tool, prefix)
		if !ok {
			t.Errorf("tool %q is not a moxy tool (want %s<name>)", tool, prefix)
			continue
		}
		names = append(names, name)
		if !slices.Contains(discoverMoxyTools, name) {
			t.Errorf("tool %q is not in the current moxy tool list", tool)
		}
	}
	for _, want := range discoverPortableCore {
		if !slices.Contains(names, want) {
			t.Errorf("tools lack the portable %s", want)
		}
	}
}

// The prompt tells the subagent to use only tools it has.
func TestDiscoverPromptNamesNoStaleTools(t *testing.T) {
	prompt := subagentPrompt(t, "discover.md")
	for _, stale := range staleDiscoverNames {
		if strings.Contains(prompt, stale) {
			t.Errorf("subagents/discover.md still names %q, which moxy does not serve", stale)
		}
	}
}

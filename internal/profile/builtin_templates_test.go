package profile

import (
	"slices"
	"strings"
	"testing"
)

// The task-typed context-floor templates (reviewer / porter / canary): each
// keeps only the servers and moxy tools its task calls, drops the claude.ai
// connectors, and disallows the builtins no worker uses. The lists come from
// transcripts of real spawned workers (circus/neat-sycamore's measurement);
// moxy's catalog is 156 tools, so every template's keep set and exclude set
// must partition it.

const moxyCatalogSize = 156

var reviewerMoxyKeep = strings.Fields(`folio_read folio_ls folio_glob folio_tree rg_search jq_jq
	man_list man_toc man_section grit_branch grit_stage smith_content-get smith_issue-get
	smith_issue-list smith_pr-get smith_pr-list smith_pr-status smith_repo-get`)

var porterMoxyExtra = strings.Fields(`folio_write folio_cp folio_mkdir grit_commit grit_remote
	chix_build chix_develop-run chix_flake-update chix_flake-lock chix_flake-check chix_log
	env_clock env_which smith_pr-create`)

var canaryMoxyKeep = strings.Fields(`folio_read folio_ls rg_search grit_stage`)

func builtinByName(t *testing.T, name string) Profile {
	t.Helper()
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("builtin profile %q missing", name)
	return Profile{}
}

func TestBuiltinTaskTemplates(t *testing.T) {
	cases := []struct {
		name        string
		servers     []string
		moxyKeep    []string
		wantExclude int
	}{
		{"reviewer", []string{"clown-builtin-jobs/troupe", "moxy/moxy"}, reviewerMoxyKeep, 138},
		{
			"porter",
			[]string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "just-us/just-us", "moxy/moxy", "spinclass/spinclass"},
			append(slices.Clone(reviewerMoxyKeep), porterMoxyExtra...), 124,
		},
		{"canary", []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "moxy/moxy"}, canaryMoxyKeep, 152},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := builtinByName(t, c.name)
			if err := Validate(p); err != nil {
				t.Fatal(err)
			}
			if p.Provider != "claude" || p.Backend != "anthropic" || p.Model != "claude-sonnet-5-5" {
				t.Errorf("shape = %s/%s/%s, want claude/anthropic/claude-sonnet-5-5", p.Provider, p.Backend, p.Model)
			}
			got := slices.Clone(p.ContextServers)
			slices.Sort(got)
			if !slices.Equal(got, c.servers) {
				t.Errorf("context_servers = %v, want %v", got, c.servers)
			}

			excluded := p.ContextExcluded["moxy/moxy"]
			if len(excluded) != c.wantExclude {
				t.Errorf("moxy excluded %d tools, want %d", len(excluded), c.wantExclude)
			}
			seen := map[string]bool{}
			for _, tool := range excluded {
				if seen[tool] {
					t.Errorf("moxy tool %q excluded twice", tool)
				}
				seen[tool] = true
			}
			for _, tool := range c.moxyKeep {
				if seen[tool] {
					t.Errorf("moxy tool %q is both kept and excluded", tool)
				}
			}
			if len(c.moxyKeep)+len(excluded) != moxyCatalogSize {
				t.Errorf("keep %d + exclude %d = %d, want the %d-tool catalog", len(c.moxyKeep), len(excluded), len(c.moxyKeep)+len(excluded), moxyCatalogSize)
			}

			if p.Env["CLOWN_DISABLE_CLAUDEAI_CONNECTORS"] != "1" {
				t.Errorf("connectors must be off: env = %v", p.Env)
			}
			disallowed := strings.Split(p.Env["CLOWN_DISALLOWED_TOOLS"], ",")
			for _, want := range []string{"Artifact", "ArtifactData", "ArtifactComments", "DesignSync", "Workflow", "SendFeedback", "EnterPlanMode", "ScheduleWakeup", "CronCreate"} {
				if !slices.Contains(disallowed, want) {
					t.Errorf("builtin %s must be disallowed", want)
				}
			}
			// Tools workers actually call must never be disallowed.
			for _, keep := range []string{"Read", "Edit", "Agent", "Skill", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate"} {
				if slices.Contains(disallowed, keep) {
					t.Errorf("builtin %s is used by workers and must not be disallowed", keep)
				}
			}
		})
	}
}

// Porter keeps spinclass only for merge / attest / description.
func TestBuiltinPorterKeepsOnlyTheMergeSurfaceOfSpinclass(t *testing.T) {
	p := builtinByName(t, "porter")
	excluded := p.ContextExcluded["spinclass/spinclass"]
	for _, kept := range []string{"merge-this-session", "merge-this-session-async", "nothing-but-the-truth", "update-this-session-description"} {
		if slices.Contains(excluded, kept) {
			t.Errorf("spinclass %s must stay available to a porter", kept)
		}
	}
	for _, dropped := range []string{"spawn-session", "close-child-session", "close-this-session", "grant-session-handle"} {
		if !slices.Contains(excluded, dropped) {
			t.Errorf("spinclass %s must be excluded for a porter", dropped)
		}
	}
}

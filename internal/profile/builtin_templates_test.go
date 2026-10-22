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

// canary-grit tests spinclass's own grit under confinement, so moxy keeps none
// of its grit (nor chix or smith) and no write tool.
var canaryGritMoxyKeep = strings.Fields(`folio_read folio_ls rg_search jq_jq`)

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
		{"reviewer", []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "moxy/moxy"}, reviewerMoxyKeep, 138},
		{
			"porter",
			[]string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "just-us/just-us", "moxy/moxy", "spinclass/spinclass"},
			append(slices.Clone(reviewerMoxyKeep), porterMoxyExtra...), 124,
		},
		{"canary", []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "moxy/moxy"}, canaryMoxyKeep, 152},
		{
			"canary-grit",
			[]string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "just-us/just-us", "moxy/moxy", "spinclass/spinclass"},
			canaryGritMoxyKeep, 152,
		},
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

// A launcher refuses a profile whose keep-set drops a server the worker needs to
// reach its driver (profilelookup.RequiredServers: job control and chat) or
// strips that server's tools, so every template must keep both whole.
func TestBuiltinTemplatesKeepTheServersAWorkerNeedsToReportBack(t *testing.T) {
	required := []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe"}
	for _, name := range []string{"reviewer", "porter", "canary", "canary-grit", "canary-spinclass"} {
		p := builtinByName(t, name)
		for _, srv := range required {
			if !slices.Contains(p.ContextServers, srv) {
				t.Errorf("%s drops %s, which a worker needs to say hello and report back", name, srv)
			}
			if len(p.ContextExcluded[srv]) > 0 {
				t.Errorf("%s strips tools from %s: %v", name, srv, p.ContextExcluded[srv])
			}
		}
	}
}

// canary-grit keeps spinclass's grit tools and close-this-session but none of
// the session-spawning or merging surface, and just-us keeps run_recipe.
func TestBuiltinCanaryGritKeepsGritAndRunRecipe(t *testing.T) {
	p := builtinByName(t, "canary-grit")
	excluded := p.ContextExcluded["spinclass/spinclass"]
	for _, kept := range []string{
		"whoami", "version", "list-handles", "close-this-session",
		"grit_branch", "grit_clone", "grit_commit", "grit_merge", "grit_remote", "grit_stage", "grit_stash", "grit_tag", "grit_worktree",
	} {
		if slices.Contains(excluded, kept) {
			t.Errorf("spinclass %s must stay available to canary-grit", kept)
		}
	}
	for _, dropped := range []string{"spawn-session", "close-child-session", "grant-session-handle", "merge-this-session", "merge-this-session-async"} {
		if !slices.Contains(excluded, dropped) {
			t.Errorf("spinclass %s must be excluded for canary-grit", dropped)
		}
	}
	if slices.Contains(p.ContextExcluded["just-us/just-us"], "run_recipe") {
		t.Error("just-us run_recipe must stay available: canaries run their probes through it")
	}
	for _, tool := range canaryGritMoxyKeep {
		if strings.HasPrefix(tool, "grit_") || strings.HasPrefix(tool, "chix_") || strings.HasPrefix(tool, "smith_") || tool == "folio_write" {
			t.Errorf("moxy %s must not be kept by canary-grit", tool)
		}
	}
}

// canary-spinclass is canary-grit without moxy: the worker's only tools beyond
// Claude Code's own are spinclass's (the same surface as canary-grit), just-us
// run_recipe, and job control and chat. Spinclass refuses canary-grit for this
// run because it keeps moxy.
func TestBuiltinCanarySpinclassDropsMoxyWhole(t *testing.T) {
	p := builtinByName(t, "canary-spinclass")
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
	got := slices.Clone(p.ContextServers)
	slices.Sort(got)
	want := []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "just-us/just-us", "spinclass/spinclass"}
	if !slices.Equal(got, want) {
		t.Errorf("context_servers = %v, want %v", got, want)
	}
	if _, ok := p.ContextExcluded["moxy/moxy"]; ok {
		t.Error("canary-spinclass carries moxy exclusions for a server it does not keep")
	}
	grit := builtinByName(t, "canary-grit")
	for _, srv := range []string{"spinclass/spinclass", "just-us/just-us"} {
		if !slices.Equal(p.ContextExcluded[srv], grit.ContextExcluded[srv]) {
			t.Errorf("%s exclusions differ from canary-grit's: %v vs %v", srv, p.ContextExcluded[srv], grit.ContextExcluded[srv])
		}
	}
	if p.Provider != grit.Provider || p.Backend != grit.Backend || p.Model != grit.Model {
		t.Errorf("shape = %s/%s/%s, want canary-grit's", p.Provider, p.Backend, p.Model)
	}
	for k, v := range grit.Env {
		if p.Env[k] != v {
			t.Errorf("env %s = %q, want canary-grit's %q", k, p.Env[k], v)
		}
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

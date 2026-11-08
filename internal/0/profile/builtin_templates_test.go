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
// moxy's catalog is 167 tools (the 156 of the original measurement, plus
// toolbox_run (moxy#500), the nine nebulous tools and issue-filer_file), so
// every template's keep set and exclude set must partition it. The partition
// is checked by name against the reviewer template's, not only by count: a
// tool added to one template's list and not another's shows up as a named
// difference. toolbox_run is in every keep set: the operator enabled the
// toolbox moxin for all profiles.

const moxyCatalogSize = 167

// moxyWritersNotInTheOriginalMeasurement are the tools added to moxy after the
// templates' lists were measured: nebulous marks stories read and imports
// OPML (writes to NewsBlur), issue-filer_file files a forge issue. No
// template that keeps moxy wants any of them.
var moxyWritersNotInTheOriginalMeasurement = strings.Fields(`nebulous_feed_query nebulous_mark_all_read
	nebulous_mark_feed_read nebulous_mark_read nebulous_opml_export nebulous_opml_import
	nebulous_resource-read nebulous_resource-templates nebulous_story_query issue-filer_file`)

var reviewerMoxyKeep = strings.Fields(`folio_read folio_ls folio_glob folio_tree rg_search jq_jq toolbox_run
	man_list man_toc man_section grit_branch grit_stage smith_content-get smith_issue-get
	smith_issue-list smith_pr-get smith_pr-list smith_pr-status smith_repo-get`)

var porterMoxyExtra = strings.Fields(`folio_write folio_cp folio_mkdir grit_commit grit_remote
	chix_build chix_develop-run chix_flake-update chix_flake-lock chix_flake-check chix_log
	env_clock env_which smith_pr-create`)

var canaryMoxyKeep = strings.Fields(`folio_read folio_ls rg_search grit_stage toolbox_run`)

// canary-grit tests spinclass's own grit under confinement, so moxy keeps none
// of its grit (nor chix or smith) and no write tool.
var canaryGritMoxyKeep = strings.Fields(`folio_read folio_ls rg_search jq_jq toolbox_run`)

// engineer-debug (clown#345) is read-only: folio read tools, rg, jq, man,
// toolbox, freud, env and smith's read verbs. just-us stays whole.
var engineerDebugMoxyKeep = strings.Fields(`folio_read folio_ls folio_glob folio_tree rg_search jq_jq toolbox_run
	man_list man_toc man_section
	freud_interruptions freud_interruptions-timeseries freud_messages freud_sessions freud_subagents
	freud_summary freud_tool-frequency freud_tool-results-summary freud_tool-results-timeseries
	freud_tool-usage freud_transcript freud_unique-tools
	env_clock env_cpu env_date env_get env_loadavg env_meminfo env_resolve env_uptime env_which
	smith_content-get smith_issue-get smith_issue-list smith_pr-get smith_pr-list smith_pr-status smith_repo-get
	smith_milestone-list smith_release-list smith_release-view smith_repo-label-list smith_tag-list smith_tag-view smith_whoami
	chix_search chix_which chix_doc chix_doc-options chix_doc-tree chix_flake-show chix_store-ls chix_store-cat
	chix_store-path-info chix_derivation-show chix_log chix_why-depends
	stats-me_query stats-me_range stats-me_series stats-me_labels stats-me_export`)

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

// moxyCatalogNames is moxy's catalog as the reviewer template partitions it:
// its keep set plus its exclusions. Every other template must partition the
// same names.
func moxyCatalogNames(t *testing.T) []string {
	t.Helper()
	names := slices.Clone(reviewerMoxyKeep)
	names = append(names, builtinByName(t, "reviewer").ContextExcluded["moxy/moxy"]...)
	slices.Sort(names)
	return slices.Compact(names)
}

func TestBuiltinTaskTemplates(t *testing.T) {
	cases := []struct {
		name        string
		servers     []string
		moxyKeep    []string
		wantExclude int
	}{
		{"reviewer", []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "moxy/moxy"}, reviewerMoxyKeep, 148},
		{
			"porter",
			[]string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "just-us/just-us", "moxy/moxy", "spinclass/spinclass"},
			append(slices.Clone(reviewerMoxyKeep), porterMoxyExtra...), 134,
		},
		{"canary", []string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "moxy/moxy"}, canaryMoxyKeep, 162},
		{
			"canary-grit",
			[]string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "just-us/just-us", "moxy/moxy", "spinclass/spinclass"},
			canaryGritMoxyKeep, 162,
		},
		{
			"engineer-debug",
			[]string{"clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "just-us/just-us", "moxy/moxy"},
			engineerDebugMoxyKeep, 105,
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
			// By name: this template's keep + exclude is the reviewer's, tool
			// for tool.
			catalog := moxyCatalogNames(t)
			for _, tool := range catalog {
				if !seen[tool] && !slices.Contains(c.moxyKeep, tool) {
					t.Errorf("moxy tool %q is neither kept nor excluded", tool)
				}
			}
			for tool := range seen {
				if !slices.Contains(catalog, tool) {
					t.Errorf("moxy tool %q is excluded but not in the catalog", tool)
				}
			}
			for _, tool := range c.moxyKeep {
				if !slices.Contains(catalog, tool) {
					t.Errorf("moxy tool %q is kept but not in the catalog", tool)
				}
			}
			for _, tool := range moxyWritersNotInTheOriginalMeasurement {
				if !seen[tool] {
					t.Errorf("moxy tool %q must be excluded", tool)
				}
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
	for _, name := range []string{"reviewer", "porter", "canary", "canary-grit", "canary-spinclass", "worker", "engineer-debug"} {
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
		if k == "CLOWN_DISALLOWED_TOOLS" {
			v += ",Agent" // no moxy, so no search tool for a subagent (clown#331)
		}
		if p.Env[k] != v {
			t.Errorf("env %s = %q, want canary-grit's %q", k, p.Env[k], v)
		}
	}
}

// clown#331: a template with no moxy gives a subagent no search tool (the
// Explore->Discover redirect names moxy's folio and rg), and claude refuses a
// subagent whose tools resolve to nothing, so the Agent tool is disallowed
// there; a template that keeps moxy keeps Agent. Changing this changes those
// profiles' digests, so a session pinned to the old digest is refused on resume
// and restart until it is re-pinned.
func TestBuiltinTemplatesWithoutMoxyDisallowTheAgentTool(t *testing.T) {
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		switch p.Name {
		case "reviewer", "porter", "canary", "canary-grit", "canary-spinclass", "worker", "engineer-debug":
		default:
			continue
		}
		keepsMoxy := slices.Contains(p.ContextServers, "moxy/moxy")
		disallowed := slices.Contains(strings.Split(p.Env["CLOWN_DISALLOWED_TOOLS"], ","), "Agent")
		if disallowed == keepsMoxy {
			t.Errorf("%s: keeps moxy %v but disallows Agent %v; want Agent disallowed exactly when moxy is dropped", p.Name, keepsMoxy, disallowed)
		}
	}
}

// worker is the confined unattended worker: canary-spinclass's surface with the
// landing tools kept. It must keep passing spinclass's confined-spawn rule
// (moxy dropped whole, just-us kept, job control and chat whole), which is
// pinned here from clown's side: a change that breaks the rule would make every
// confined spawn on this profile refuse.
func TestBuiltinWorkerIsCanarySpinclassPlusTheLandingTools(t *testing.T) {
	w := builtinByName(t, "worker")
	if err := Validate(w); err != nil {
		t.Fatal(err)
	}
	got := slices.Clone(w.ContextServers)
	slices.Sort(got)
	// canary-spinclass's servers plus the frontend-served issue-filer moxin
	// (cmd/clown/frontendmoxin.go names it "<plugin>/<moxin>") and the
	// diagnostics server that serves plugin disconnect records (clown#344).
	want := []string{"clown-builtin-diagnostics/disconnects", "clown-builtin-jobs/ringmaster", "clown-builtin-jobs/troupe", "clown-builtin-moxins/issue-filer", "just-us/just-us", "spinclass/spinclass"}
	if !slices.Equal(got, want) {
		t.Fatalf("context_servers = %v, want %v", got, want)
	}
	if len(w.ContextExcluded["clown-builtin-moxins/issue-filer"]) > 0 {
		t.Error("worker strips tools from the issue-filer moxin it keeps for filing")
	}
	if slices.Contains(w.ContextServers, "moxy/moxy") {
		t.Error("worker must drop moxy whole: a confined spawn refuses a profile that keeps it")
	}
	if _, ok := w.ContextExcluded["moxy/moxy"]; ok {
		t.Error("worker carries moxy exclusions for a server it does not keep")
	}
	if slices.Contains(w.ContextExcluded["just-us/just-us"], "run_recipe") {
		t.Error("just-us run_recipe must stay available")
	}
	excluded := w.ContextExcluded["spinclass/spinclass"]
	for _, kept := range []string{
		"merge-this-session", "merge-this-session-async", "nothing-but-the-truth", "update-this-session-description",
		"whoami", "version", "list-handles", "close-this-session", "host-load",
		"restart-this-session", "cancel-restart",
		"grit_branch", "grit_clone", "grit_commit", "grit_merge", "grit_remote", "grit_stage", "grit_stash", "grit_tag", "grit_worktree",
	} {
		if slices.Contains(excluded, kept) {
			t.Errorf("spinclass %s must stay available to worker", kept)
		}
	}
	for _, dropped := range []string{"spawn-session", "close-child-session", "grant-session-handle", "release-session-handle", "session-job-cancel"} {
		if !slices.Contains(excluded, dropped) {
			t.Errorf("spinclass %s must be excluded for worker", dropped)
		}
	}
	// Against canary-spinclass: the only difference in the spinclass
	// exclusions is the four landing tools coming off the list.
	cs := builtinByName(t, "canary-spinclass")
	landing := []string{"merge-this-session", "merge-this-session-async", "nothing-but-the-truth", "update-this-session-description"}
	var wantExcluded []string
	for _, tool := range cs.ContextExcluded["spinclass/spinclass"] {
		if !slices.Contains(landing, tool) {
			wantExcluded = append(wantExcluded, tool)
		}
	}
	gotExcluded := slices.Clone(excluded)
	slices.Sort(wantExcluded)
	slices.Sort(gotExcluded)
	if !slices.Equal(gotExcluded, wantExcluded) {
		t.Errorf("spinclass exclusions = %v, want canary-spinclass's minus the landing tools = %v", gotExcluded, wantExcluded)
	}
	if !slices.Equal(w.ContextExcluded["just-us/just-us"], cs.ContextExcluded["just-us/just-us"]) {
		t.Error("just-us exclusions differ from canary-spinclass's")
	}
	if w.Provider != cs.Provider || w.Backend != cs.Backend || w.Model != cs.Model {
		t.Errorf("shape = %s/%s/%s, want canary-spinclass's", w.Provider, w.Backend, w.Model)
	}
	for k, v := range cs.Env {
		if k == "CLOWN_DISALLOWED_TOOLS" {
			// The one builtin worker may call that canary-spinclass may not
			// (TestBuiltinWorkerMayReadMCPResources).
			v = strings.ReplaceAll(v, ",ReadMcpResourceTool", "")
		}
		if w.Env[k] != v {
			t.Errorf("env %s = %q, want canary-spinclass's %q", k, w.Env[k], v)
		}
	}
	if w.Env["CLOWN_DISABLE_CLAUDEAI_CONNECTORS"] != "1" {
		t.Error("no worker profile turns the claude.ai connectors on")
	}
}

// Operator ruling 2026-11-05: worker alone may read MCP resources by URI
// (ringmaster#43 serves a job's output and records that way, scoped to the
// caller's channel). The wake names the URI, so listing stays disallowed; every
// other template is unchanged. Allowing it changed worker's digest: a session
// pinned to the old one is refused on resume and restart until re-pinned.
func TestBuiltinWorkerMayReadMCPResources(t *testing.T) {
	resourceTools := []string{"ListMcpResourcesTool", "ReadMcpResourceTool", "ReadMcpResourceDirTool"}
	w := strings.Split(builtinByName(t, "worker").Env["CLOWN_DISALLOWED_TOOLS"], ",")
	if slices.Contains(w, "ReadMcpResourceTool") {
		t.Error("worker must be allowed ReadMcpResourceTool")
	}
	for _, still := range []string{"ListMcpResourcesTool", "ReadMcpResourceDirTool"} {
		if !slices.Contains(w, still) {
			t.Errorf("worker must still disallow %s", still)
		}
	}
	for _, name := range []string{"reviewer", "porter", "canary", "canary-grit", "canary-spinclass"} {
		disallowed := strings.Split(builtinByName(t, name).Env["CLOWN_DISALLOWED_TOOLS"], ",")
		for _, tool := range resourceTools {
			if !slices.Contains(disallowed, tool) {
				t.Errorf("%s must still disallow %s: only worker reads MCP resources", name, tool)
			}
		}
	}
}

// engineer-debug is read-only (clown#345): it keeps no write or side-effect
// moxy tool, and, like worker, may read MCP resources by URI while listing
// stays disallowed.
func TestBuiltinEngineerDebugIsReadOnly(t *testing.T) {
	for _, tool := range engineerDebugMoxyKeep {
		for _, writer := range []string{"folio_write", "folio_rm", "folio_mv", "folio_cp", "folio_chmod", "folio_link", "folio_mkdir"} {
			if tool == writer {
				t.Errorf("engineer-debug keeps %s", tool)
			}
		}
		for _, prefix := range []string{"grit_", "chrest_", "gmail_", "car_", "piers_", "gws_", "calendar_", "prison_"} {
			if strings.HasPrefix(tool, prefix) {
				t.Errorf("engineer-debug keeps %s", tool)
			}
		}
		// chix keeps only its read-only subset: nothing that builds, runs,
		// locks, updates or changes the store.
		for _, mutator := range []string{
			"chix_build", "chix_run", "chix_develop-run", "chix_flake-check", "chix_flake-lock",
			"chix_flake-init", "chix_flake-update", "chix_store-gc", "chix_store-realize", "chix_nix-hash",
		} {
			if tool == mutator {
				t.Errorf("engineer-debug keeps %s", tool)
			}
		}
		switch tool {
		case "async", "async-cancel", "async-result", "batch", "restart":
			t.Errorf("engineer-debug keeps %s", tool)
		}
		if strings.HasPrefix(tool, "smith_") {
			for _, verb := range []string{"create", "close", "comment", "edit", "merge", "delete", "watch"} {
				if strings.Contains(tool, "-"+verb) {
					t.Errorf("engineer-debug keeps smith write verb %s", tool)
				}
			}
		}
	}
	p := builtinByName(t, "engineer-debug")
	disallowed := strings.Split(p.Env["CLOWN_DISALLOWED_TOOLS"], ",")
	if slices.Contains(disallowed, "ReadMcpResourceTool") {
		t.Error("engineer-debug must be allowed ReadMcpResourceTool")
	}
	for _, still := range []string{"ListMcpResourcesTool", "ReadMcpResourceDirTool"} {
		if !slices.Contains(disallowed, still) {
			t.Errorf("engineer-debug must still disallow %s", still)
		}
	}
	if len(p.ContextExcluded["just-us/just-us"]) > 0 {
		t.Error("engineer-debug keeps just-us whole")
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

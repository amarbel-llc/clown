package main

import (
	"log/slog"
	"reflect"
	"testing"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

func TestGroupToolsByPrefix(t *testing.T) {
	d := pluginhost.DiscoveredServer{PluginName: "moxy", ServerName: "moxy"}

	cases := []struct {
		name  string
		tools []pluginhost.ToolInfo
		want  []toolGroup
	}{
		{
			// moxy's ACTUAL rendering (internal/naming.Template,
			// amarbel-llc/moxy): dot-separated "<group>.<tool>". Confirmed
			// live against a real moxy instance (198-tool catalog) — this is
			// the primary case, not the mangled-prefix fallback below, since
			// FetchToolCatalog talks to moxy's own /mcp endpoint directly,
			// bypassing Claude Code's tool-name mangling entirely.
			name: "moxy-style multi-group (dotted rendering)",
			tools: []pluginhost.ToolInfo{
				{Name: "folio.read"},
				{Name: "folio.glob"},
				{Name: "grit.status"},
			},
			want: []toolGroup{
				{name: "folio", tools: []string{"folio.read", "folio.glob"}},
				{name: "grit", tools: []string{"grit.status"}},
			},
		},
		{
			// Fallback case: a server whose /mcp responses happen to already
			// carry Claude Code's mangled prefix form (not known to occur
			// today, but handled defensively).
			name: "mangled-prefix fallback",
			tools: []pluginhost.ToolInfo{
				{Name: "mcp__plugin_moxy_moxy__folio_read"},
				{Name: "mcp__plugin_moxy_moxy__folio_glob"},
				{Name: "mcp__plugin_moxy_moxy__grit_status"},
			},
			want: []toolGroup{
				{name: "folio", tools: []string{"mcp__plugin_moxy_moxy__folio_read", "mcp__plugin_moxy_moxy__folio_glob"}},
				{name: "grit", tools: []string{"mcp__plugin_moxy_moxy__grit_status"}},
			},
		},
		{
			name: "unprefixed name falls into ungrouped bucket",
			tools: []pluginhost.ToolInfo{
				{Name: "some_other_tool"},
			},
			want: []toolGroup{
				{name: "", tools: []string{"some_other_tool"}},
			},
		},
		{
			name: "prefixed but no further segment falls into ungrouped bucket",
			tools: []pluginhost.ToolInfo{
				{Name: "mcp__plugin_moxy_moxy__bare"},
			},
			want: []toolGroup{
				{name: "", tools: []string{"mcp__plugin_moxy_moxy__bare"}},
			},
		},
		{
			name:  "empty catalog",
			tools: nil,
			want:  []toolGroup{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := groupToolsByPrefix(d, tc.tools)
			if len(got) == 0 {
				got = []toolGroup{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("groupToolsByPrefix() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCheapContextShouldActivate(t *testing.T) {
	cases := []struct {
		name  string
		flag  bool
		saved *profile.Profile
		want  bool
	}{
		{"flag alone activates (interactive picker)", true, nil, true},
		{"no flag, no profile: inactive", false, nil, false},
		{"no flag, profile with no saved selection: inactive", false, &profile.Profile{Name: "plain"}, false},
		{
			"no flag, profile WITH a saved selection: activates without the flag",
			false, &profile.Profile{Name: "trimmed", ContextServers: []string{"moxy/moxy"}}, true,
		},
		{
			"flag AND a saved selection: still activates",
			true, &profile.Profile{Name: "trimmed", ContextServers: []string{"moxy/moxy"}}, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cheapContextShouldActivate(tc.flag, tc.saved); got != tc.want {
				t.Errorf("cheapContextShouldActivate(%v, %+v) = %v, want %v", tc.flag, tc.saved, got, tc.want)
			}
		})
	}
}

// TestPromptSaveSelection_EmptyKeptRefusesWithoutPrompting guards the fix
// for a real data-loss bug: profile.Profile.ContextServers uses
// `toml:"...,omitempty"`, and BurntSushi/toml's omitempty drops ANY
// zero-length slice (nil or not) on write. Without this guard, saving a
// selection where every server was deselected would silently produce a
// profile indistinguishable on disk from one with no saved selection at
// all — the opposite of the user's saved intent, with no error.
//
// This must return an error WITHOUT running the interactive huh confirm
// form at all (the test process has no TTY) — proving the empty check is a
// guard clause ahead of any prompting, not just documentation.
func TestPromptSaveSelection_EmptyKeptRefusesWithoutPrompting(t *testing.T) {
	err := promptSaveSelection(selectionResult{excludedTools: map[string][]string{}}, cheapContextSaveContext{})
	if err == nil {
		t.Fatal("expected an error for a selection with zero kept servers")
	}
}

func TestSelectionFromSavedProfile(t *testing.T) {
	moxy := pluginhost.DiscoveredServer{PluginName: "moxy", ServerName: "moxy"}
	caldav := pluginhost.DiscoveredServer{PluginName: "bob", ServerName: "caldav"}
	catalogs := []serverCatalog{
		{server: moxy, groups: []toolGroup{
			{name: "folio", tools: []string{"folio.read", "folio.glob"}},
			{name: "grit", tools: []string{"grit.status"}},
		}},
		{server: caldav, groups: nil},
	}

	t.Run("nil profile falls back to picker", func(t *testing.T) {
		_, ok := selectionFromSavedProfile(nil, catalogs)
		if ok {
			t.Fatal("nil profile should report ok=false (fall back to interactive picker)")
		}
	})

	t.Run("profile with no saved selection falls back to picker", func(t *testing.T) {
		_, ok := selectionFromSavedProfile(&profile.Profile{Name: "plain"}, catalogs)
		if ok {
			t.Fatal("profile with nil ContextServers should report ok=false")
		}
	})

	t.Run("keeps only saved servers, drops the rest", func(t *testing.T) {
		saved := &profile.Profile{Name: "trimmed", ContextServers: []string{"moxy/moxy"}}
		result, ok := selectionFromSavedProfile(saved, catalogs)
		if !ok {
			t.Fatal("expected ok=true for a profile with a saved selection")
		}
		if len(result.kept) != 1 || result.kept[0].Name() != "moxy/moxy" {
			t.Fatalf("kept = %#v, want just moxy/moxy", result.kept)
		}
	})

	t.Run("applies exclusions for tools present in the live catalog", func(t *testing.T) {
		saved := &profile.Profile{
			Name:            "trimmed",
			ContextServers:  []string{"moxy/moxy"},
			ContextExcluded: map[string][]string{"moxy/moxy": {"grit.status"}},
		}
		result, ok := selectionFromSavedProfile(saved, catalogs)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if !reflect.DeepEqual(result.excludedTools["moxy/moxy"], []string{"grit.status"}) {
			t.Errorf("excludedTools = %#v, want [grit.status]", result.excludedTools)
		}
	})

	t.Run("silently drops a saved tool name no longer in the live catalog", func(t *testing.T) {
		saved := &profile.Profile{
			Name:           "trimmed",
			ContextServers: []string{"moxy/moxy"},
			ContextExcluded: map[string][]string{
				"moxy/moxy": {"grit.status", "folio.renamed_or_removed"},
			},
		}
		result, ok := selectionFromSavedProfile(saved, catalogs)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if !reflect.DeepEqual(result.excludedTools["moxy/moxy"], []string{"grit.status"}) {
			t.Errorf("excludedTools = %#v, want only [grit.status] (stale name silently dropped)", result.excludedTools)
		}
	})

	t.Run("silently drops a saved server no longer discovered", func(t *testing.T) {
		saved := &profile.Profile{
			Name:           "trimmed",
			ContextServers: []string{"moxy/moxy", "removed/server"},
		}
		result, ok := selectionFromSavedProfile(saved, catalogs)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if len(result.kept) != 1 || result.kept[0].Name() != "moxy/moxy" {
			t.Fatalf("kept = %#v, want just moxy/moxy (removed/server silently dropped)", result.kept)
		}
	})
}

func TestSelectServers_SavedSelectionSkipsPickerAndTTYRequirement(t *testing.T) {
	// selectServers normally requires an interactive TTY (pluginhost.IsInteractive).
	// A saved-selection replay must work even when that's false, since test
	// processes are never interactive — this exercises exactly that path.
	moxy := pluginhost.DiscoveredServer{PluginName: "moxy", ServerName: "moxy"}
	catalogs := []serverCatalog{{server: moxy, groups: []toolGroup{{name: "folio", tools: []string{"folio.read"}}}}}
	saved := &profile.Profile{Name: "trimmed", ContextServers: []string{"moxy/moxy"}}

	result, err := selectServers(catalogs, slog.Default(), saved, cheapContextSaveContext{})
	if err != nil {
		t.Fatalf("selectServers with a saved selection should not require a TTY: %v", err)
	}
	if len(result.kept) != 1 || result.kept[0].Name() != "moxy/moxy" {
		t.Fatalf("kept = %#v, want just moxy/moxy", result.kept)
	}
}

func TestIsMultiGroup(t *testing.T) {
	cases := []struct {
		name   string
		groups []toolGroup
		want   bool
	}{
		{"no groups", nil, false},
		{"ungrouped only", []toolGroup{{name: "", tools: []string{"a", "b"}}}, false},
		{"single named group", []toolGroup{{name: "folio", tools: []string{"a"}}}, false},
		{
			"multiple named groups",
			[]toolGroup{{name: "folio", tools: []string{"a"}}, {name: "grit", tools: []string{"b"}}},
			true,
		},
		{
			"named groups plus ungrouped",
			[]toolGroup{{name: "folio", tools: []string{"a"}}, {name: "", tools: []string{"b"}}, {name: "grit", tools: []string{"c"}}},
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMultiGroup(tc.groups); got != tc.want {
				t.Errorf("isMultiGroup() = %v, want %v", got, tc.want)
			}
		})
	}
}

package main

import (
	"reflect"
	"testing"

	"github.com/amarbel-llc/clown/internal/pluginhost"
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


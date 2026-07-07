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
			name: "moxy-style multi-group",
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

func TestExcludeToolsPayload(t *testing.T) {
	cases := []struct {
		name   string
		groups []toolGroup
		want   []string
	}{
		{
			name: "named group: bare name plus every mangled tool name",
			groups: []toolGroup{
				{name: "folio", tools: []string{"mcp__plugin_moxy_moxy__folio_read", "mcp__plugin_moxy_moxy__folio_glob"}},
			},
			// "folio" lets moxy's toolexclude.Parse exclude the whole moxin;
			// the mangled names let clown-stdio-bridge's exact-match filter
			// work too — each endpoint ignores entries it doesn't recognize.
			want: []string{"folio", "mcp__plugin_moxy_moxy__folio_read", "mcp__plugin_moxy_moxy__folio_glob"},
		},
		{
			name:   "ungrouped bucket contributes only tool names, no bare \"\" entry",
			groups: []toolGroup{{name: "", tools: []string{"some_tool"}}},
			want:   []string{"some_tool"},
		},
		{
			name: "multiple groups concatenate in order",
			groups: []toolGroup{
				{name: "folio", tools: []string{"t1"}},
				{name: "grit", tools: []string{"t2", "t3"}},
			},
			want: []string{"folio", "t1", "grit", "t2", "t3"},
		},
		{
			name:   "no groups",
			groups: nil,
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := excludeToolsPayload(tc.groups)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("excludeToolsPayload() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

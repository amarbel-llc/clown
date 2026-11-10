package catalogsnap

import (
	"encoding/json"
	"testing"
)

// The harness calls a tool by a mangled name, mcp__plugin_<plugin>_<server>__<tool>,
// with characters outside [A-Za-z0-9_-] in the advertised tool name replaced by
// "_" (observed: moxy's folio.read is mcp__plugin_moxy_moxy__folio_read).
// An enforcer that sees only that name maps it back through the snapshot.
func TestPresentRecordsTheNameTheHarnessCallsEachToolBy(t *testing.T) {
	got := Present(fixture(), contract(t))
	for path, want := range map[string]string{
		"moxy/folio/read":                    "mcp__plugin_moxy_moxy__folio_read",
		"moxy/smith/issue-list":              "mcp__plugin_moxy_moxy__smith_issue-list",
		"moxy/moxy/status":                   "mcp__plugin_moxy_moxy__status",
		"just-us/just-us/run_recipe":         "mcp__plugin_just-us_just-us__run_recipe",
		"clown-builtin-harness/harness/Read": "Read",
	} {
		n, ok := find(got, path)
		if !ok {
			t.Fatalf("%s not presented", path)
		}
		if n.HarnessName != want {
			t.Errorf("%s harness name %q, want %q", path, n.HarnessName, want)
		}
	}
}

func TestResolveMapsAHarnessNameBackToItsCatalogIDs(t *testing.T) {
	s, c := fixture(), contract(t)
	for name, want := range map[string]string{
		"mcp__plugin_moxy_moxy__folio_read":       "moxy/folio/read",
		"mcp__plugin_moxy_moxy__smith_issue-list": "moxy/smith/issue-list",
		"mcp__plugin_just-us_just-us__run_recipe": "just-us/just-us/run_recipe",
		"Read": "clown-builtin-harness/harness/Read",
	} {
		n, ok := Resolve(s, c, name)
		if !ok || n.Plugin+"/"+n.Server+"/"+n.Tool != want {
			t.Errorf("Resolve(%s) = %+v, %v; want %s", name, n, ok, want)
		}
	}
	for _, name := range []string{"mcp__plugin_moxy_moxy__folio_write", "Nope", ""} {
		if n, ok := Resolve(s, c, name); ok {
			t.Errorf("Resolve(%q) = %+v; want no match", name, n)
		}
	}
}

// moxy publishes its canonical server, tool and perms-request in _meta
// (moxy#517); they win over the "." split and the "[perms: X]" suffix.
func TestMoxyMetaKeysWinOverTheNameSplitAndThePermsSuffix(t *testing.T) {
	s := Snapshot{Principal: "p", Servers: []Server{{Plugin: "moxy", Server: "moxy", Tools: []Tool{{
		Name:        "fs-read",
		Description: "[perms: each-use]",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}}}`),
		Meta:        json.RawMessage(`{"moxy/server":"folio","moxy/tool":"read","moxy/perms-request":"always-allow"}`),
	}}}}}
	got := Present(s, contract(t))
	if len(got) != 1 {
		t.Fatalf("Present = %+v", got)
	}
	n := got[0]
	if n.Plugin+"/"+n.Server+"/"+n.Tool != "moxy/folio/read" || n.Perm != PermAllow {
		t.Errorf("presented %s/%s/%s perm %q, want moxy/folio/read perm allow", n.Plugin, n.Server, n.Tool, n.Perm)
	}
	if n.HarnessName != "mcp__plugin_moxy_moxy__fs-read" {
		t.Errorf("harness name %q: the harness still calls the tool by its advertised name", n.HarnessName)
	}
	if len(n.Inputs) != 1 || n.Inputs[0].FieldKind != "path" {
		t.Errorf("inputs %+v: the field kinds follow the canonical ids", n.Inputs)
	}
}

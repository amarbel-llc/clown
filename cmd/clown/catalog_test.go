package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/catalogsnap"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

func TestCatalogServersKeepsTheBoundServersWhoseToolsWereFetched(t *testing.T) {
	started := []*pluginhost.ManagedServer{
		{Name: "moxy/moxy"}, {Name: "just-us/just-us"}, {Name: "dropped/dropped"}, {Name: "silent/silent"},
	}
	kept := []pluginhost.DiscoveredServer{
		{PluginName: "moxy", ServerName: "moxy"}, {PluginName: "just-us", ServerName: "just-us"}, {PluginName: "silent", ServerName: "silent"},
	}
	fetch := func(srv *pluginhost.ManagedServer) ([]pluginhost.ToolInfo, bool) {
		switch srv.Name {
		case "moxy/moxy":
			return []pluginhost.ToolInfo{{Name: "folio.read", Description: "[perms: dynamic]", InputSchema: json.RawMessage(`{"type":"object"}`), Meta: json.RawMessage(`{"kind":"read"}`)}}, true
		case "just-us/just-us":
			return []pluginhost.ToolInfo{{Name: "run_recipe"}}, true
		}
		return nil, false
	}
	got := catalogServers(started, kept, fetch)
	if len(got) != 2 || got[0].Plugin != "moxy" || got[0].Server != "moxy" || got[1].Plugin != "just-us" {
		t.Fatalf("catalogServers = %+v, want moxy/moxy and just-us/just-us", got)
	}
	tool := got[0].Tools[0]
	if tool.Name != "folio.read" || string(tool.InputSchema) != `{"type":"object"}` || string(tool.Meta) != `{"kind":"read"}` || tool.Description != "[perms: dynamic]" {
		t.Errorf("tool not carried verbatim: %+v", tool)
	}
}

func TestCatalogHarnessListsBuiltinsForClaudeOnly(t *testing.T) {
	if catalogHarness("claude") != catalogsnap.HarnessClaudeCode || catalogHarness("opencode") == catalogsnap.HarnessClaudeCode {
		t.Error("catalogHarness: only claude is the claude-code harness")
	}
}

func TestCatalogVerbRefusesAnythingButTraversalServe(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCatalogVerb([]string{"list"}, nil, &out, &errb); code != 2 || out.Len() != 0 {
		t.Errorf("clown catalog list: exit %d stdout %q", code, out.String())
	}
}

// Without the cookie the verb writes nothing to stdout (RFC 0013 launch).
func TestCatalogTraversalServeWithoutTheCookieExitsNonZeroQuietly(t *testing.T) {
	t.Setenv("TRAVERSAL_PLUGIN_COOKIE", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := runCatalogVerb([]string{"traversal-serve"}, bytes.NewReader(nil), &out, &errb); code == 0 || out.Len() != 0 {
		t.Errorf("exit %d stdout %q, want non-zero and nothing", code, out.String())
	}
}

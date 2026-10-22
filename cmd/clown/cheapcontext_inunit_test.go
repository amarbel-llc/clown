package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

// Servers a confined (--agent-scope) launch reserves for the unit: the
// frontend never starts them, so they have no started entry and no catalog.
var inUnitTestServers = []pluginhost.DiscoveredServer{
	{PluginName: "moxy", ServerName: "moxy"},
	{PluginName: "just-us", ServerName: "just-us"},
}

// TestApplyCheapContextSelection_ConfinedBuiltinsKeepInUnitServers is the
// clown#290 regression: before the fix, the saved-profile replay saw only
// frontend-started servers, so a confined worker under any builtin lost moxy
// and just-us even when the profile kept them.
func TestApplyCheapContextSelection_ConfinedBuiltinsKeepInUnitServers(t *testing.T) {
	builtins, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range builtins {
		if p.ContextServers == nil {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			kept, excluded, err := applyCheapContextSelection(
				context.Background(), &pluginhost.Host{}, inUnitTestServers, nil, inUnitTestServers,
				slog.Default(), &p, cheapContextSaveContext{})
			if err != nil {
				t.Fatal(err)
			}

			var want, got []string
			for _, d := range inUnitTestServers {
				if slices.Contains(p.ContextServers, d.Name()) {
					want = append(want, d.Name())
				}
			}
			for _, d := range kept {
				got = append(got, d.Name())
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("kept in-unit servers = %v, want %v", got, want)
			}
			for _, name := range want {
				if !reflect.DeepEqual(excluded[name], p.ContextExcluded[name]) {
					t.Errorf("exclusions for %s = %v, want the saved %v verbatim", name, excluded[name], p.ContextExcluded[name])
				}
			}
		})
	}
}

// TestApplyCheapContextSelection_CanaryGritKeepsJustUs pins the reported case
// (a confined canary-grit worker with no just-us and no moxy tools).
func TestApplyCheapContextSelection_CanaryGritKeepsJustUs(t *testing.T) {
	builtins, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(builtins, func(p profile.Profile) bool { return p.Name == "canary-grit" })
	if i < 0 {
		t.Fatal("no builtin canary-grit")
	}
	kept, excluded, err := applyCheapContextSelection(
		context.Background(), &pluginhost.Host{}, inUnitTestServers, nil, inUnitTestServers,
		slog.Default(), &builtins[i], cheapContextSaveContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Fatalf("kept = %v, want moxy/moxy and just-us/just-us", kept)
	}
	if !slices.Contains(excluded["moxy/moxy"], "grit_branch") {
		t.Errorf("moxy exclusions = %v, want them to carry grit_branch", excluded["moxy/moxy"])
	}
	if slices.Contains(excluded["just-us/just-us"], "run_recipe") {
		t.Errorf("just-us exclusions = %v, must not drop run_recipe", excluded["just-us/just-us"])
	}
}

func TestResolveExcludedTools(t *testing.T) {
	live := []string{"folio.read", "grit.branch", "smith.content-get", "status", "jq.jq"}
	saved := []string{"grit_branch", "smith_content-get", "status", "gone_tool", "jq.jq"}
	got := resolveExcludedTools(saved, live)
	want := []string{"grit.branch", "smith.content-get", "status", "jq.jq"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveExcludedTools = %v, want %v", got, want)
	}
	if got := unmatchedExclusions(saved, live); !reflect.DeepEqual(got, []string{"gone_tool"}) {
		t.Errorf("unmatchedExclusions = %v, want [gone_tool]", got)
	}
}

// moxyRendered renders a claude-side moxy tool name the way moxy's default
// "{server}.{tool}" template does: the server is the part before the first
// "_" (no moxy server name contains one); a meta tool has no server.
func moxyRendered(claudeName string) string {
	server, tool, ok := strings.Cut(claudeName, "_")
	if !ok {
		return claudeName
	}
	return server + "." + tool
}

// TestBuiltinMoxyExclusionsResolveAgainstMoxyRendering is the clown#291
// regression: the builtins list moxy tools as claude shows them ("grit_branch"),
// moxy renders "grit.branch", and exact matching excluded nothing.
func TestBuiltinMoxyExclusionsResolveAgainstMoxyRendering(t *testing.T) {
	builtins, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range builtins {
		saved := p.ContextExcluded["moxy/moxy"]
		if len(saved) == 0 {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			var live, want []string
			for _, name := range saved {
				live = append(live, moxyRendered(name))
				want = append(want, moxyRendered(name))
			}
			live = append(live, "folio.read", "rg.search") // kept by every template
			if got := resolveExcludedTools(saved, live); !reflect.DeepEqual(got, want) {
				t.Errorf("resolved %d of %d saved names; missing %v", len(got), len(saved), slices.DeleteFunc(slices.Clone(want), func(n string) bool { return slices.Contains(got, n) }))
			}
		})
	}
}

// TestApplyInUnitExclusionsPushesRenderedNames drives the relay's push against
// a fake server speaking MCP initialize/tools/list and /clown/exclude-tools.
func TestApplyInUnitExclusionsPushesRenderedNames(t *testing.T) {
	var pushed []string
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{}}`, req.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"tools":[{"name":"folio.read"},{"name":"grit.branch"},{"name":"chix.build"}]}}`, req.ID)
		}
	})
	mux.HandleFunc("/clown/exclude-tools", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Exclude []string `json:"exclude"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		pushed = body.Exclude
		_ = json.NewEncoder(w).Encode(body)
	})
	fake := httptest.NewServer(mux)
	defer fake.Close()

	srv := (&pluginhost.Host{}).Reserve(pluginhost.DiscoveredServer{PluginName: "moxy", ServerName: "moxy"}, strings.TrimPrefix(fake.URL, "http://"))
	applyInUnitExclusions(srv, []string{"grit_branch", "chix_build", "smith_pr-create"}, slog.Default())

	if want := []string{"grit.branch", "chix.build"}; !reflect.DeepEqual(pushed, want) {
		t.Errorf("pushed %v, want %v", pushed, want)
	}
}

func TestNarrowInUnitServers(t *testing.T) {
	servers := []agentscope.InUnitServer{{Name: "moxy/moxy"}, {Name: "just-us/just-us"}}
	kept := []pluginhost.DiscoveredServer{{PluginName: "moxy", ServerName: "moxy"}}
	got := narrowInUnitServers(servers, kept, map[string][]string{"moxy/moxy": {"grit_branch"}})
	want := []agentscope.InUnitServer{{Name: "moxy/moxy", Exclude: []string{"grit_branch"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("narrowInUnitServers = %#v, want %#v", got, want)
	}
}

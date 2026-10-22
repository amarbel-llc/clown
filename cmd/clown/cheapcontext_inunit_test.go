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
	"sync"
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
			out, err := applyCheapContextSelection(
				context.Background(), &pluginhost.Host{}, inUnitTestServers, nil, inUnitTestServers,
				slog.Default(), &p, cheapContextSaveContext{})
			if err != nil {
				t.Fatal(err)
			}
			excluded := out.inUnitExcluded

			var want, got []string
			for _, d := range inUnitTestServers {
				if slices.Contains(p.ContextServers, d.Name()) {
					want = append(want, d.Name())
				}
			}
			for _, d := range out.kept {
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
	out, err := applyCheapContextSelection(
		context.Background(), &pluginhost.Host{}, inUnitTestServers, nil, inUnitTestServers,
		slog.Default(), &builtins[i], cheapContextSaveContext{})
	if err != nil {
		t.Fatal(err)
	}
	excluded := out.inUnitExcluded
	if len(out.kept) != 2 {
		t.Fatalf("kept = %v, want moxy/moxy and just-us/just-us", out.kept)
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

// fakeExcludeServer speaks MCP initialize/tools/list and /clown/exclude-tools
// the way moxy does: a pushed name stops being listed, unless ignorePush
// (a server whose filter does not take) or failPush (the route errors).
type fakeExcludeServer struct {
	mu         sync.Mutex
	tools      []string
	pushed     []string
	ignorePush bool
	failPush   bool
}

func (f *fakeExcludeServer) start(t *testing.T) *httptest.Server {
	t.Helper()
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
			f.mu.Lock()
			var tools []map[string]string
			for _, name := range f.tools {
				if f.ignorePush || !slices.Contains(f.pushed, name) {
					tools = append(tools, map[string]string{"name": name})
				}
			}
			f.mu.Unlock()
			raw, _ := json.Marshal(tools)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"tools":%s}}`, req.ID, raw)
		}
	})
	mux.HandleFunc("/clown/exclude-tools", func(w http.ResponseWriter, r *http.Request) {
		if f.failPush {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		var body struct {
			Exclude []string `json:"exclude"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.pushed = body.Exclude
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func reserveFake(name string, fake *httptest.Server) *pluginhost.ManagedServer {
	plugin, server, _ := strings.Cut(name, "/")
	return (&pluginhost.Host{}).Reserve(pluginhost.DiscoveredServer{PluginName: plugin, ServerName: server}, strings.TrimPrefix(fake.URL, "http://"))
}

var fakeMoxyTools = []string{"folio.read", "grit.branch", "chix.build"}

// TestApplyInUnitExclusionsPushesRenderedNames drives the relay's push and
// readback against a fake moxy.
func TestApplyInUnitExclusionsPushesRenderedNames(t *testing.T) {
	f := &fakeExcludeServer{tools: fakeMoxyTools}
	rb := applyInUnitExclusions(reserveFake("moxy/moxy", f.start(t)), []string{"grit_branch", "chix_build", "smith_pr-create"}, slog.Default())

	if want := []string{"grit.branch", "chix.build"}; !reflect.DeepEqual(f.pushed, want) {
		t.Errorf("pushed %v, want %v", f.pushed, want)
	}
	if rb.Problem != "" || rb.CatalogAfter != 1 || !reflect.DeepEqual(rb.Unmatched, []string{"smith_pr-create"}) {
		t.Errorf("readback = %+v, want confirmed with 1 tool left and smith_pr-create unmatched", rb)
	}
	if err := exclusionRefusal("p", []exclusionReadback{rb}); err != nil {
		t.Errorf("a confirmed readback refused: %v", err)
	}
	// smith_pr-create is the harmless kind: no live tool under any rendering.
	if len(rb.Ambiguous) != 0 {
		t.Errorf("ambiguous = %v, want none", rb.Ambiguous)
	}
}

// TestApplyAndReadBackExclusionsRefuses covers each way a profile's exclusions
// go unconfirmed; every one must refuse the launch.
func TestApplyAndReadBackExclusionsRefuses(t *testing.T) {
	cases := []struct {
		name  string
		fake  *fakeExcludeServer
		saved []string
		want  string
	}{
		{"no saved name matches", &fakeExcludeServer{tools: fakeMoxyTools}, []string{"smith_pr-create"}, "matches"},
		{"push fails", &fakeExcludeServer{tools: fakeMoxyTools, failPush: true}, []string{"grit_branch"}, "pushing the exclusions failed"},
		{"readback still lists it", &fakeExcludeServer{tools: fakeMoxyTools, ignorePush: true}, []string{"grit_branch"}, "still listed"},
		// Fix 5 (audit 2026-10-22): an unmatched name that could mean a live
		// tool, and a name that matches more than one.
		{"unmatched by case", &fakeExcludeServer{tools: fakeMoxyTools}, []string{"chix_build", "GRIT_BRANCH"}, `"GRIT_BRANCH" matches no tool exactly but could mean grit.branch`},
		{"unmatched by claude's prefix", &fakeExcludeServer{tools: fakeMoxyTools}, []string{"mcp__plugin_moxy_moxy__chix_build"}, "could mean chix.build"},
		{"unmatched by separator", &fakeExcludeServer{tools: []string{"chix.flake-check"}}, []string{"chix_flake_check"}, "could mean chix.flake-check"},
		{"matches two tools", &fakeExcludeServer{tools: []string{"a.b_c", "a_b.c"}}, []string{"a_b_c"}, `"a_b_c" matches 2 tools: a.b_c, a_b.c`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := reserveFake("moxy/moxy", c.fake.start(t))
			rb := applyAndReadBackExclusions(context.Background(), &pluginhost.Host{}, srv, c.saved, c.fake.tools)
			if !strings.Contains(rb.Problem, c.want) {
				t.Errorf("problem = %q, want it to mention %q", rb.Problem, c.want)
			}
			if exclusionRefusal("p", []exclusionReadback{rb}) == nil {
				t.Error("an unconfirmed readback did not refuse")
			}
		})
	}
	t.Run("catalog not fetched", func(t *testing.T) {
		rb := applyAndReadBackExclusions(context.Background(), &pluginhost.Host{}, &pluginhost.ManagedServer{Name: "moxy/moxy"}, []string{"grit_branch"}, nil)
		if rb.Problem == "" {
			t.Error("a server with no fetched catalog was confirmed")
		}
	})
}

// TestApplyCheapContextSelection_ProfileRefusesUnconfirmedExclusions drives
// the frontend path: a started server under a saved selection whose
// exclusions do not take refuses the launch, and the readback is returned for
// the record either way.
func TestApplyCheapContextSelection_ProfileRefusesUnconfirmedExclusions(t *testing.T) {
	saved := &profile.Profile{
		Name:            "p",
		ContextServers:  []string{"moxy/moxy"},
		ContextExcluded: map[string][]string{"moxy/moxy": {"grit_branch"}},
	}
	moxy := pluginhost.DiscoveredServer{PluginName: "moxy", ServerName: "moxy"}
	for _, c := range []struct {
		name       string
		ignorePush bool
		refuse     bool
	}{{"confirmed", false, false}, {"readback differs", true, true}} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeExcludeServer{tools: fakeMoxyTools, ignorePush: c.ignorePush}
			host := &pluginhost.Host{}
			srv := host.Reserve(moxy, strings.TrimPrefix(f.start(t).URL, "http://"))
			out, err := applyCheapContextSelection(context.Background(), host, []pluginhost.DiscoveredServer{moxy},
				[]*pluginhost.ManagedServer{srv}, nil, slog.Default(), saved, cheapContextSaveContext{})
			if (err != nil) != c.refuse {
				t.Fatalf("err = %v, want refusal %v", err, c.refuse)
			}
			if len(out.readbacks) != 1 || !reflect.DeepEqual(out.readbacks[0].Matched, []string{"grit.branch"}) {
				t.Errorf("readbacks = %+v, want one for moxy matching grit.branch", out.readbacks)
			}
		})
	}
}

// An in-unit server's CLOWN_BIN is this clown, not one inherited into the
// frontend's env (round R: two clown store paths in one unit).
func TestReserveInUnitServersUseThisClown(t *testing.T) {
	host := &pluginhost.Host{BaseEnv: map[string]string{"CLOWN_BIN": "/nix/store/old/bin/clown", "CLOWN_SESSION_ID": "k"}}
	servers := reserveInUnitServers(host, []pluginhost.DiscoveredServer{{PluginName: "moxy", ServerName: "moxy"}}, nil)
	if len(servers) != 1 {
		t.Fatalf("servers = %+v", servers)
	}
	if got := servers[0].Env["CLOWN_BIN"]; got != clownExePath() {
		t.Errorf("CLOWN_BIN = %q, want this clown %q", got, clownExePath())
	}
	if _, ok := servers[0].Env["CLOWN_SESSION_ID"]; ok {
		t.Error("CLOWN_SESSION_ID reached an in-unit server's env")
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

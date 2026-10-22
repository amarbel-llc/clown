package main

import (
	"context"
	"log/slog"
	"reflect"
	"slices"
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

func TestNarrowInUnitServers(t *testing.T) {
	servers := []agentscope.InUnitServer{{Name: "moxy/moxy"}, {Name: "just-us/just-us"}}
	kept := []pluginhost.DiscoveredServer{{PluginName: "moxy", ServerName: "moxy"}}
	got := narrowInUnitServers(servers, kept, map[string][]string{"moxy/moxy": {"grit_branch"}})
	want := []agentscope.InUnitServer{{Name: "moxy/moxy", Exclude: []string{"grit_branch"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("narrowInUnitServers = %#v, want %#v", got, want)
	}
}

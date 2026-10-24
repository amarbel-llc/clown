package main

import (
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/pluginhost"
)

// F12 (FDR 0020): under --agent-scope only spinclass's bridged frontend
// server moves to a per-session unix socket; every other server, ringmaster
// and troupe included, is unchanged.
func TestRouteServeSocketsRewritesOnlyTheSpinclassServe(t *testing.T) {
	const bridge = "/nix/store/b/bin/clown-stdio-bridge"
	bridged := func(plugin, server, scope string) pluginhost.DiscoveredServer {
		return pluginhost.DiscoveredServer{PluginName: plugin, ServerName: server, Def: pluginhost.ServerDef{
			Command: bridge, Args: []string{"--command", "/bin/" + server, "--", "serve"}, Scope: scope,
		}}
	}
	discovered := []pluginhost.DiscoveredServer{
		bridged("spinclass", "spinclass", pluginhost.ScopeFrontend),
		bridged("clown-builtin-jobs", "ringmaster", pluginhost.ScopeFrontend),
		bridged("moxy", "moxy", ""),
		{PluginName: "spinclass", ServerName: "native", Def: pluginhost.ServerDef{Command: "/bin/native", Scope: pluginhost.ScopeFrontend}},
	}
	orig := slices.Clone(discovered[0].Def.Args)

	got, relays, err := routeServeSockets(discovered, bridge, "/run/user/1001", "p1", "u-1.service")
	if err != nil {
		t.Fatal(err)
	}
	const sock = "/run/user/1001/clown/serve/p1/spinclass.sock"
	wantArgs := append([]string{"--listen-unix", sock, "--peer-unit", "u-1.service"}, orig...)
	if !slices.Equal(got[0].Def.Args, wantArgs) {
		t.Errorf("spinclass args = %q, want %q", got[0].Def.Args, wantArgs)
	}
	if !slices.Equal(discovered[0].Def.Args, orig) {
		t.Error("the caller's discovered slice was mutated")
	}
	for i := 1; i < len(got); i++ {
		if !slices.Equal(got[i].Def.Args, discovered[i].Def.Args) {
			t.Errorf("%s changed: %q", got[i].Name(), got[i].Def.Args)
		}
	}
	want := map[string]agentscope.Relay{"spinclass/spinclass": {Listen: "127.0.0.1:19000", Socket: sock}}
	if len(relays) != 1 || relays["spinclass/spinclass"] != want["spinclass/spinclass"] {
		t.Errorf("relays = %+v, want %+v", relays, want)
	}
}

func TestRouteServeSocketsRefusesWithoutARuntimeDir(t *testing.T) {
	d := []pluginhost.DiscoveredServer{{PluginName: "spinclass", ServerName: "spinclass", Def: pluginhost.ServerDef{Command: "/b", Scope: pluginhost.ScopeFrontend}}}
	if _, _, err := routeServeSockets(d, "/b", "", "p1", "u-1.service"); err == nil {
		t.Error("want refusal: no XDG_RUNTIME_DIR, nowhere to put the socket")
	}
}

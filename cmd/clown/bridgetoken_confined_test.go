package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/alfa/agentscope"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

// A confined-launch fixture for the bridge token (clown#288, confined slice):
// a plugin with one frontend-scope bridge and one agent-side bridge, discovered
// with a token as a --agent-scope launch does. The frontend bridge keeps the
// token in its env (the unit reaches it through a byte-splicing relay, which
// passes the manifest's Authorization header through); the agent-side bridge is
// reserved for the unit, its servers-file entry carries the same env so the
// in-unit relay starts it enforcing the token, and its manifest entry carries
// the header; with no token nothing carries one.
func TestConfinedLaunchCarriesTheBridgeTokenToBothSidesOfTheRelay(t *testing.T) {
	const (
		bridge = "/nix/store/b/bin/clown-stdio-bridge"
		token  = "confined-token-0123456789abcdef"
	)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"fixture"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":1,"stdioServers":{"fe":{"command":"/bin/true","scope":"frontend"},"iu":{"command":"/bin/true"}}}`
	if err := os.WriteFile(filepath.Join(dir, "clown.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	discover := func(tok string) (*pluginhost.Host, []pluginhost.DiscoveredServer) {
		h := &pluginhost.Host{PluginDirs: []string{dir}, BridgePath: bridge, BridgeToken: tok}
		found, err := h.Discover()
		if err != nil {
			t.Fatal(err)
		}
		return h, found
	}
	byName := func(found []pluginhost.DiscoveredServer, server string) pluginhost.DiscoveredServer {
		for _, d := range found {
			if d.ServerName == server {
				return d
			}
		}
		t.Fatalf("no server %q in %v", server, found)
		return pluginhost.DiscoveredServer{}
	}

	host, found := discover(token)
	if got := byName(found, "fe").Def.Env[pluginhost.EnvBridgeToken]; got != token {
		t.Errorf("the frontend bridge's env token = %q, want the token", got)
	}
	inUnit := reserveInUnitServers(host, found, nil)
	if len(inUnit) != 1 || !strings.HasSuffix(inUnit[0].Name, "/iu") {
		t.Fatalf("in-unit servers = %+v, want only the agent-side bridge", inUnit)
	}
	if got := inUnit[0].Env[pluginhost.EnvBridgeToken]; got != token {
		t.Errorf("the in-unit bridge's env token = %q, want the token (the relay starts it with this env)", got)
	}
	var reservedEntry pluginhost.MCPServerEntry
	for _, s := range host.Servers {
		if s.Reserved() {
			reservedEntry = host.ServerEntry(s)
		}
	}
	if reservedEntry.Headers["Authorization"] != "Bearer "+token {
		t.Errorf("the reserved bridge's manifest entry headers = %v, want the bearer token", reservedEntry.Headers)
	}

	// The servers file the unit reads is owner-only, and it is where the unit
	// can see the token for the in-unit bridge: the same value the manifest
	// already gives the agent.
	file := filepath.Join(t.TempDir(), "in-unit-servers.json")
	if err := agentscope.WriteInUnitServers(file, inUnit); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("servers file mode = %v, want 0600", fi.Mode().Perm())
	}
	back, err := agentscope.ReadInUnitServers(file)
	if err != nil {
		t.Fatal(err)
	}
	if back[0].Env[pluginhost.EnvBridgeToken] != token {
		t.Errorf("servers file round trip lost the token: %+v", back)
	}

	// With the opt-in off a confined launch has no token anywhere.
	offHost, offFound := discover("")
	for _, d := range offFound {
		if _, ok := d.Def.Env[pluginhost.EnvBridgeToken]; ok {
			t.Errorf("%s has a token with the opt-in off", d.Name())
		}
	}
	for _, s := range reserveInUnitServers(offHost, offFound, nil) {
		if _, ok := s.Env[pluginhost.EnvBridgeToken]; ok {
			t.Errorf("in-unit %s has a token with the opt-in off", s.Name)
		}
	}
	for _, s := range offHost.Servers {
		if s.Reserved() && offHost.ServerEntry(s).Headers != nil {
			t.Error("a reserved entry has headers with the opt-in off")
		}
	}
}

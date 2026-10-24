package main

import (
	"fmt"
	"slices"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/pluginhost"
)

// servePlugin is the plugin whose frontend server is moved to a serve socket
// under --agent-scope (F12, FDR 0020): spinclass serve classifies its callers
// from its own session, so only the session's own unit may reach it.
const servePlugin = "spinclass"

// routeServeSockets moves, under --agent-scope, spinclass's bridged frontend
// server from loopback TCP to the session's serve socket: its bridge gets
// --listen-unix <socket> --peer-unit <unit>, so it admits only the unit (and
// this clown) and has no TCP listener. It returns a copy of discovered with
// that server's args rewritten, and the relay the unit needs for it: the
// in-unit port claude's manifest names, spliced to the socket. Every other
// server (ringmaster, troupe, in-unit servers) is unchanged. The bridge keeps
// one child for every connection, as on TCP (FDR 0020 F12; clown#286).
func routeServeSockets(discovered []pluginhost.DiscoveredServer, bridgePath, runtimeDir, principal, unit string) ([]pluginhost.DiscoveredServer, map[string]agentscope.Relay, error) {
	out := slices.Clone(discovered)
	relays := map[string]agentscope.Relay{}
	for i, d := range out {
		if d.PluginName != servePlugin || d.Def.Scope != pluginhost.ScopeFrontend || d.Def.Command != bridgePath {
			continue
		}
		if len(relays) > 0 {
			return nil, nil, fmt.Errorf("plugin %s declares more than one frontend server; one serve socket per session is supported", servePlugin)
		}
		sock, err := agentscope.ServeSocketPath(runtimeDir, principal, d.ServerName)
		if err != nil {
			return nil, nil, fmt.Errorf("serve socket for %s: %w", d.Name(), err)
		}
		out[i].Def.Args = append([]string{"--listen-unix", sock, "--peer-unit", unit}, d.Def.Args...)
		relays[d.Name()] = agentscope.Relay{Listen: fmt.Sprintf("127.0.0.1:%d", agentscope.ServePortBase), Socket: sock}
	}
	return out, relays, nil
}

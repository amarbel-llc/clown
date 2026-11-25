package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

// bridgeSwitchEnv is the operator's switch for the bridge (FDR 0048): set to
// "1" in the environment of the clown that launches the session, never read
// from the agent's side. Unset (the default) means no bridge: nothing is
// created and the serve's environment is untouched. The listener in serve has
// its own second gate (a trusted `[bridge] enable`), so setting this alone
// starts nothing there.
const bridgeSwitchEnv = "CLOWN_BRIDGE_V1"

// sessionBridge is a session's bridge directory: clown creates it (0700) and
// names it to the serve; the serve creates <dir>/bridge.sock. Zero: no bridge.
type sessionBridge struct {
	Dir    string
	Socket string
}

// routeBridge, when the switch is on, creates <runtime>/clown/launch/<key>/bridge
// (0700) and gives the spinclass serve bridge CLOWN_BRIDGE_DIR and
// CLOWN_BRIDGE_UNIT in its environment (unit gets the .service suffix the
// cgroup directory has). It returns a copy of discovered with only that
// server's Env changed. Any problem (switch off, no serve, no runtime dir, a
// key that is not one path segment, a socket path over the unix limit) means
// "no bridge": the error says why, the caller logs it, and the launch goes on.
func routeBridge(discovered []pluginhost.DiscoveredServer, bridgePath, runtimeDir, key, unit string, switchOn bool) ([]pluginhost.DiscoveredServer, sessionBridge, error) {
	if !switchOn {
		return discovered, sessionBridge{}, nil
	}
	if !filepath.IsAbs(runtimeDir) {
		return discovered, sessionBridge{}, fmt.Errorf("bridge off: XDG_RUNTIME_DIR %q is not absolute", runtimeDir)
	}
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, "/\x00") || unit == "" {
		return discovered, sessionBridge{}, fmt.Errorf("bridge off: key %q or unit %q unusable", key, unit)
	}
	out := slices.Clone(discovered)
	var b sessionBridge
	for i, d := range out {
		if !isServeServer(d, bridgePath) {
			continue
		}
		if b.Dir != "" {
			return discovered, sessionBridge{}, fmt.Errorf("bridge off: plugin %s declares more than one frontend server", servePlugin)
		}
		dir := filepath.Join(runtimeDir, "clown", "launch", key, "bridge")
		sock, err := bridgewire.SocketPath(dir)
		if err != nil {
			return discovered, sessionBridge{}, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return discovered, sessionBridge{}, fmt.Errorf("bridge off: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return discovered, sessionBridge{}, fmt.Errorf("bridge off: %w", err)
		}
		env := maps.Clone(d.Def.Env)
		if env == nil {
			env = map[string]string{}
		}
		env[bridgewire.EnvDir] = dir
		env[bridgewire.EnvUnit] = strings.TrimSuffix(unit, ".service") + ".service"
		out[i].Def.Env = env
		b = sessionBridge{Dir: dir, Socket: sock}
	}
	if b.Dir == "" {
		return discovered, sessionBridge{}, nil
	}
	return out, b, nil
}

// cleanup removes a socket the serve left behind and the directory, when
// nothing else is in it. A zero bridge is a no-op.
func (b sessionBridge) cleanup() {
	if b.Dir == "" {
		return
	}
	_ = os.Remove(b.Socket)
	_ = os.Remove(b.Dir)
}

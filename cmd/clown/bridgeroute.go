package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

// bridgeSwitchEnv is the operator's switch for the bridge (FDR 0048): set to
// "1" in the environment of the clown that launches the session, never read
// from the agent's side. Unset (the default) means no bridge: nothing is
// created, and the serve is told so explicitly (its three bridge variables
// set empty, so a value in a parent environment cannot turn its listener on).
// The listener in serve has its own second gate (a trusted `[bridge] enable`),
// so setting this alone starts nothing there.
const bridgeSwitchEnv = "CLOWN_BRIDGE_V1"

// sessionBridge is a session's bridge: clown creates the directory (0700) and
// names it to the serve; the serve creates <Dir>/bridge.sock. CgroupFile is
// where clown writes the unit's exact cgroup once it is known. Zero: no bridge.
type sessionBridge struct {
	Dir        string
	Socket     string
	CgroupFile string
}

// bridgeEnvKeys are the serve's three bridge variables.
var bridgeEnvKeys = []string{bridgewire.EnvDir, bridgewire.EnvUnit, bridgewire.EnvCgroupFile}

// bridgeOff returns a copy of discovered in which the serve's bridge variables
// are set empty: the answer to "is the bridge off" is then written, not
// inferred from absence.
func bridgeOff(discovered []pluginhost.DiscoveredServer, bridgePath string) []pluginhost.DiscoveredServer {
	out := slices.Clone(discovered)
	for i, d := range out {
		if !isServeServer(d, bridgePath) {
			continue
		}
		env := maps.Clone(d.Def.Env)
		if env == nil {
			env = map[string]string{}
		}
		for _, k := range bridgeEnvKeys {
			env[k] = ""
		}
		out[i].Def.Env = env
	}
	return out
}

// routeBridge, when the switch is on, creates <runtime>/clown/launch/<key>/bridge
// (0700) and gives the spinclass serve bridge CLOWN_BRIDGE_DIR,
// CLOWN_BRIDGE_UNIT (the .service suffix the cgroup directory has) and
// CLOWN_BRIDGE_CGROUP_FILE in its environment. It returns a copy of discovered
// with only that server's Env changed. Any problem (switch off, no serve, no
// runtime dir, a key that is not one path segment, a socket path over the unix
// limit) means "no bridge": the returned servers then carry the three variables
// empty, the error (if any) says why, the caller logs it, and the launch goes
// on.
func routeBridge(discovered []pluginhost.DiscoveredServer, bridgePath, runtimeDir, key, unit string, switchOn bool) ([]pluginhost.DiscoveredServer, sessionBridge, error) {
	off := func(err error) ([]pluginhost.DiscoveredServer, sessionBridge, error) {
		return bridgeOff(discovered, bridgePath), sessionBridge{}, err
	}
	if !switchOn {
		return off(nil)
	}
	if !filepath.IsAbs(runtimeDir) {
		return off(fmt.Errorf("bridge off: XDG_RUNTIME_DIR %q is not absolute", runtimeDir))
	}
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, "/\x00") || unit == "" {
		return off(fmt.Errorf("bridge off: key %q or unit %q unusable", key, unit))
	}
	out := slices.Clone(discovered)
	var b sessionBridge
	for i, d := range out {
		if !isServeServer(d, bridgePath) {
			continue
		}
		if b.Dir != "" {
			return off(fmt.Errorf("bridge off: plugin %s declares more than one frontend server", servePlugin))
		}
		launchDir := filepath.Join(runtimeDir, "clown", "launch", key)
		dir := filepath.Join(launchDir, "bridge")
		sock, err := bridgewire.SocketPath(dir)
		if err != nil {
			return off(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return off(fmt.Errorf("bridge off: %w", err))
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return off(fmt.Errorf("bridge off: %w", err))
		}
		cgFile := filepath.Join(launchDir, bridgewire.CgroupFileName)
		env := maps.Clone(d.Def.Env)
		if env == nil {
			env = map[string]string{}
		}
		env[bridgewire.EnvDir] = dir
		env[bridgewire.EnvUnit] = strings.TrimSuffix(unit, ".service") + ".service"
		env[bridgewire.EnvCgroupFile] = cgFile
		out[i].Def.Env = env
		b = sessionBridge{Dir: dir, Socket: sock, CgroupFile: cgFile}
	}
	if b.Dir == "" {
		return discovered, sessionBridge{}, nil
	}
	return out, b, nil
}

// writeCgroup records the unit's exact cgroup for the serve to read per
// connection: 0600, a regular file written to a temporary name and renamed over
// (so a reader sees the old value or the new, never a part), replacing a
// symlink rather than following one. It is called again on a relaunch that
// makes a new unit.
func (b sessionBridge) writeCgroup(cgroup string) error {
	if b.CgroupFile == "" {
		return nil
	}
	if !strings.HasPrefix(cgroup, "/") || strings.HasSuffix(cgroup, "/") || strings.ContainsAny(cgroup, "\n\x00") {
		return fmt.Errorf("bridge: unusable cgroup %q", cgroup)
	}
	tmp := b.CgroupFile + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(cgroup) // no trailing newline: the file is exactly the path
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, b.CgroupFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// bridgeSocketWait is how long the launch waits for the serve to create
// bridge.sock before it decides the session has no bridge (the serve's own gate
// may be off, or it may have refused).
var bridgeSocketWait = 10 * time.Second

// awaitSocket reports whether the serve created a unix socket at b.Socket
// within timeout, polling. A zero bridge is never ready.
func (b sessionBridge) awaitSocket(timeout time.Duration) bool {
	if b.Socket == "" {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		if fi, err := os.Lstat(b.Socket); err == nil && fi.Mode().Type() == os.ModeSocket {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// cleanup removes a socket the serve left behind, the cgroup file, and the
// directory, when nothing else is in it. A zero bridge is a no-op.
func (b sessionBridge) cleanup() {
	if b.Dir == "" {
		return
	}
	_ = os.Remove(b.Socket)
	_ = os.Remove(b.Dir)
	if b.CgroupFile != "" {
		_ = os.Remove(b.CgroupFile)
	}
}

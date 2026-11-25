package main

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
	"code.linenisgreat.com/clown/internal/alfa/agentscope"
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
	// Unit is the agent unit's name (".service" included); the cgroup written
	// must end in it.
	Unit string
	// written is what this process wrote to CgroupFile (shared by copies).
	written *writtenCgroup
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
		cgFile := filepath.Join(launchDir, bridgewire.CgroupFileName)
		if err := prepareBridgeDir(launchDir, dir, sock, cgFile); err != nil {
			return off(fmt.Errorf("bridge off: %w", err))
		}
		env := maps.Clone(d.Def.Env)
		if env == nil {
			env = map[string]string{}
		}
		env[bridgewire.EnvDir] = dir
		env[bridgewire.EnvUnit] = strings.TrimSuffix(unit, ".service") + ".service"
		env[bridgewire.EnvCgroupFile] = cgFile
		out[i].Def.Env = env
		b = sessionBridge{Dir: dir, Socket: sock, CgroupFile: cgFile, Unit: strings.TrimSuffix(unit, ".service") + ".service", written: &writtenCgroup{}}
	}
	if b.Dir == "" {
		return discovered, sessionBridge{}, nil
	}
	return out, b, nil
}

// prepareBridgeDir makes the launch's bridge directory ready for a fresh
// serve and trusts nothing an earlier launch of this key left (round 1, A-M3,
// B-M3): the launch directory and the bridge directory must each be a real
// directory owned by this user (a symlink or foreign directory is refused, never
// followed), and a socket, cgroup file or temporary file left by a frontend that
// was killed is removed first, so a stale file can neither admit the old unit
// nor pass for the new serve's socket.
func prepareBridgeDir(launchDir, dir, sock, cgFile string) error {
	if err := os.MkdirAll(launchDir, 0o700); err != nil {
		return err
	}
	for _, d := range []string{launchDir, dir} {
		fi, err := os.Lstat(d)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return err
		case !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is not a real directory", d)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			return fmt.Errorf("%s is owned by uid %d, not this user", d, st.Uid)
		}
	}
	for _, f := range []string{sock, cgFile, cgFile + ".tmp"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing the stale %s: %w", f, err)
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// writtenCgroup is the value this process last wrote to the cgroup file, so
// cleanup removes only its own file.
type writtenCgroup struct {
	mu  sync.Mutex
	val string
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
	// Identity, not just shape (round 1, A-L5): the value must be this agent
	// unit's own cgroup, so a tooling fault (CLOWN_SYSTEMCTL is an override)
	// cannot publish a broad path such as the user manager's.
	if b.Unit != "" && !strings.HasSuffix(cgroup, "/"+agentscope.Slice+"/"+b.Unit) {
		return fmt.Errorf("bridge: cgroup %q is not this unit's (%s/%s)", cgroup, agentscope.Slice, b.Unit)
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
	if b.written != nil {
		b.written.mu.Lock()
		b.written.val = cgroup
		b.written.mu.Unlock()
	}
	return nil
}

// orphan is what clown does when it drops the bridge after the serve started
// (a covering writable mount, a late socket): the serve cannot be told through
// its environment any more, so the socket and the cgroup file are removed. With
// no cgroup file the serve admits nobody (it fails closed on a missing file);
// with no socket nothing can connect. Logged, because the serve's listener is
// still running (round 1, A-M1).
func (b sessionBridge) orphan(logger *slog.Logger) {
	if b.Dir == "" {
		return
	}
	_ = os.Remove(b.Socket)
	_ = os.Remove(b.CgroupFile)
	_ = os.Remove(b.CgroupFile + ".tmp")
	if logger != nil {
		logger.Warn("bridge: dropped after the serve started; its socket and cgroup file are removed and its listener is orphaned until the session ends", "dir", b.Dir)
	}
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
	// The cgroup file only if it still holds what this process wrote: a
	// frontend that took over the key since must not lose its own (round 1, B-L1).
	if b.CgroupFile != "" && b.written != nil {
		b.written.mu.Lock()
		mine := b.written.val
		b.written.mu.Unlock()
		if got, err := os.ReadFile(b.CgroupFile); mine != "" && err == nil && string(got) == mine {
			_ = os.Remove(b.CgroupFile)
		}
	}
}

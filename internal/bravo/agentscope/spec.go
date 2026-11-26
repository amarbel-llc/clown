// Package agentscope launches a provider (claude) in an AGENT SCOPE: a
// transient systemd user service with a private mount, pid and devpts
// namespace, the worktree bind-mounted in, and nothing of the operator's home,
// session bus or display reachable (clown#244, FDR 0020). clown itself stays in
// the frontend scope and relays the terminal.
//
// The spec is spinclass's (FDR 0035): what the unit must see. The result is
// what spinclass's confinement check needs: the unit name and its cgroup.
package agentscope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"code.linenisgreat.com/clown/internal/0/unitresult"
)

// SpecVersion is the only spec version this build understands.
const SpecVersion = 1

// Socket is one unix socket the unit may reach, bound in as a single inode at
// the same path.
type Socket struct {
	Path    string `json:"path"`
	Purpose string `json:"purpose"`
}

// Spec is what the unit must see, written by spinclass (FDR 0035 Interface).
type Spec struct {
	Version      int               `json:"version"`
	Worktree     string            `json:"worktree"`
	OperatorHome string            `json:"operator_home"`
	UnitNameHint string            `json:"unit_name_hint"`
	ResultPath   string            `json:"result_path"`
	BindsRW      []string          `json:"binds_rw"`
	BindsRO      []string          `json:"binds_ro"`
	Sockets      []Socket          `json:"sockets"`
	DenyPaths    []string          `json:"deny_paths"`
	EgressAllow  []string          `json:"egress_allow"`
	Env          map[string]string `json:"env"`
	// Bridge is the ONE switch of the bridge (FDR 0048), set by spinclass from a
	// trusted-only knob: "" or absent is OFF (no bridge offered, nothing
	// created); "offered": on, and the launch continues if the bridge cannot be
	// set up (`bridge: off: <reason>` in the result and on stderr); "required":
	// on, and the launch is REFUSED if it cannot, with the same reason, before
	// the unit's provider starts. A unit whose only way out is the bridge must
	// not start without one.
	Bridge string `json:"bridge,omitempty"`
}

// The Bridge modes a spec may name.
const (
	BridgeOffered  = "offered"
	BridgeRequired = "required"
)

// BridgeIsRequired reports whether the spec makes a missing bridge refuse the
// launch.
func (s Spec) BridgeIsRequired() bool { return s.Bridge == BridgeRequired }

// LoadSpec reads and validates a spec. Unknown fields are refused: a field
// this build does not understand may be one it was meant to enforce.
func LoadSpec(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("agent scope spec: %w", err)
	}
	return ParseSpec(path, raw)
}

// ParseSpec is LoadSpec over bytes already read from path, so a caller can
// hash exactly the bytes it validated.
func ParseSpec(path string, raw []byte) (Spec, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("agent scope spec %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Spec{}, fmt.Errorf("agent scope spec %s: %w", path, err)
	}
	return s, nil
}

// Validate refuses a spec clown cannot honour without weakening the boundary.
func (s Spec) Validate() error {
	if s.Version != SpecVersion {
		return fmt.Errorf("version %d unsupported (this clown understands %d)", s.Version, SpecVersion)
	}
	switch s.Bridge {
	case "", BridgeOffered, BridgeRequired:
	default:
		return fmt.Errorf("bridge %q: must be %q or %q", s.Bridge, BridgeOffered, BridgeRequired)
	}
	if err := requireAbs("worktree", s.Worktree); err != nil {
		return err
	}
	if err := requireAbs("operator_home", s.OperatorHome); err != nil {
		return err
	}
	if s.ResultPath != "" {
		if err := requireAbs("result_path", s.ResultPath); err != nil {
			return err
		}
		// The unit could forge a result it can write (e.g. point pty at the
		// operator's pts so a check skips it).
		for _, rw := range append([]string{s.Worktree}, s.BindsRW...) {
			if s.ResultPath == rw || strings.HasPrefix(s.ResultPath, rw+"/") {
				return fmt.Errorf("result_path %q is writable from inside the unit (under %q)", s.ResultPath, rw)
			}
		}
	}
	exposed := map[string][]string{
		"worktree": {s.Worktree},
		"binds_rw": s.BindsRW,
		"binds_ro": s.BindsRO,
	}
	for _, sock := range s.Sockets {
		exposed["sockets"] = append(exposed["sockets"], sock.Path)
	}
	for field, paths := range exposed {
		for _, p := range paths {
			if err := requireBindable(field, p); err != nil {
				return err
			}
			if exposesWhole(p, s.OperatorHome) {
				return fmt.Errorf("%s entry %q exposes the whole operator home %q", field, p, s.OperatorHome)
			}
			// The operator's nix.conf can carry access-tokens (clown#272).
			if nixConf := filepath.Join(s.OperatorHome, ".config", "nix"); exposesWhole(p, nixConf) || within(p, nixConf) {
				return fmt.Errorf("%s entry %q exposes the operator's nix configuration %q", field, p, nixConf)
			}
			// clown binds the operator's claude credential read-only; a
			// writable bind over it would undo that.
			if creds := filepath.Join(s.OperatorHome, ".claude", ".credentials.json"); field != "binds_ro" && field != "sockets" && exposesWhole(p, creds) {
				return fmt.Errorf("%s entry %q makes the operator's claude credential %q writable", field, p, creds)
			}
		}
	}
	// clown's state holds the confinement records (clown#318); a unit that
	// could write it could rewrite its own session's record. Checked after
	// the operator-home refusals above, which name the broader exposure.
	// Compared lexically and with symlinks resolved, so neither a bind nor
	// the state dir reached through a symlink slips past.
	clownState := filepath.Join(OperatorXDG(os.Environ(), s.OperatorHome, "XDG_STATE_HOME"), "clown")
	realState := ResolvePath(clownState)
	for _, p := range s.writableBinds() {
		real := ResolvePath(p)
		if exposesWhole(p, clownState) || within(p, clownState) || exposesWhole(real, realState) || within(real, realState) {
			return fmt.Errorf("worktree, binds_rw or sockets entry %q makes clown's state %q writable from inside the unit", p, clownState)
		}
	}
	// Every session's launch directory (<runtime>/clown/launch/<key>) holds its
	// plugin sockets and, with the bridge, bridge.sock and bridge.cgroup: a
	// worktree or binds_rw entry covering or inside the launch root would let the
	// unit write another session's cgroup file and reach its sockets (FDR 0048,
	// round 1, A-M1). Refused, not continued.
	if rt := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(rt) {
		launchRoot := filepath.Join(rt, "clown", "launch")
		realLaunch := ResolvePath(launchRoot)
		// Every writable bind, `sockets` included (they are bound read-write and
		// may be any file type), and also the read-only ones: a read-only bind of
		// the root makes every session's socket connectable from the unit
		// (round 2, M1).
		for _, p := range append(s.writableBinds(), s.BindsRO...) {
			real := ResolvePath(p)
			if exposesWhole(p, launchRoot) || within(p, launchRoot) || exposesWhole(real, realLaunch) || within(real, realLaunch) {
				return fmt.Errorf("worktree, binds_rw, sockets or binds_ro entry %q exposes the launch directories %q to the unit", p, launchRoot)
			}
		}
	}
	// The issue-filer service token lives in $XDG_STATE_HOME/circus and is read
	// only by the frontend-side moxin; the unit keeps the operator's uid, so
	// only the mount namespace keeps it out. No bind of any kind (read-only
	// too) may cover it or sit inside it.
	circusState := filepath.Join(OperatorXDG(os.Environ(), s.OperatorHome, "XDG_STATE_HOME"), "circus")
	realCircus := ResolvePath(circusState)
	for field, paths := range exposed {
		for _, p := range paths {
			real := ResolvePath(p)
			if exposesWhole(p, circusState) || within(p, circusState) || exposesWhole(real, realCircus) || within(real, realCircus) {
				return fmt.Errorf("%s entry %q exposes %q, which holds the issue-filer service token the frontend uses for the unit", field, p, circusState)
			}
		}
	}
	for _, p := range s.DenyPaths {
		if err := requireBindable("deny_paths", p); err != nil {
			return err
		}
	}
	for k := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("env key %q is not a valid variable name", k)
		}
		if k == "SSH_AUTH_SOCK" {
			// The one scrubbed key a spec may set: the session's own key holder
			// (its socket is in sockets[]). It must name a socket the spec binds,
			// so no variable points at a path that is not in the unit, and the
			// operator's agent is never named by accident or by a typo.
			if !slices.ContainsFunc(s.Sockets, func(so Socket) bool { return so.Path == s.Env[k] }) {
				return fmt.Errorf("env SSH_AUTH_SOCK=%q names no socket the spec binds (sockets[]); the unit has no agent unless the spec binds one", s.Env[k])
			}
			continue
		}
		if scrubbedKeys[k] {
			return fmt.Errorf("env key %q is scrubbed from the agent scope and may not be set by the spec", k)
		}
	}
	return nil
}

// writableBinds is every path of the spec the unit gets a read-write bind of:
// the worktree, binds_rw and each socket (BindPaths= is read-write whatever the
// file type).
func (s Spec) writableBinds() []string {
	out := append([]string{s.Worktree}, s.BindsRW...)
	for _, sock := range s.Sockets {
		out = append(out, sock.Path)
	}
	return out
}

// maxSymlinkHops bounds ResolvePath, as the kernel bounds a lookup (40).
const maxSymlinkHops = 40

// ResolvePath is p with every symlink on it followed, component by
// component, whether or not the link's target exists: a dangling link
// resolves to where it points, and a component that does not exist is kept
// as written. A loop, or more than maxSymlinkHops links, stops the walk; the
// rest is then appended unresolved (such a path cannot be bound, so the
// unit cannot reach anything through it). The result is absolute and clean.
func ResolvePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	rest := strings.Split(filepath.Clean(p), "/")
	cur := "/"
	for hops := 0; len(rest) > 0; {
		comp := rest[0]
		rest = rest[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, comp)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		target, err := os.Readlink(next)
		if hops++; err != nil || hops > maxSymlinkHops {
			return filepath.Join(append([]string{next}, rest...)...)
		}
		if filepath.IsAbs(target) {
			cur = "/"
		}
		rest = append(strings.Split(target, "/"), rest...)
	}
	return cur
}

func requireAbs(field, p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("%s %q must be an absolute, clean path", field, p)
	}
	return nil
}

// requireBindable additionally refuses ':' and whitespace, which systemd's
// BindPaths= syntax ("src[:dst[:opts]]", space-separated) would misparse.
func requireBindable(field, p string) error {
	if err := requireAbs(field, p); err != nil {
		return err
	}
	if strings.ContainsAny(p, ": \t\n") {
		return fmt.Errorf("%s %q contains ':' or whitespace, which systemd bind syntax cannot carry", field, p)
	}
	return nil
}

// exposesWhole reports whether binding p would expose all of target: p is
// target itself or one of its ancestors.
func exposesWhole(p, target string) bool {
	if p == target || p == "/" {
		return true
	}
	return strings.HasPrefix(target, p+"/")
}

// Capabilities is what this build's launcher actually does, reported by
// `clown agent-scope-capabilities` and echoed in every Result. spinclass
// refuses a confined launch unless the ones it requires are listed, so a
// capability is added here only with the code that provides it.
var Capabilities = []string{
	"agent-unit",            // transient user service, mount/pid/devpts namespaces
	"private-network",       // PrivateNetwork=yes, always
	"egress-proxy",          // frontend CONNECT proxy with a host:port allowlist
	"frontend-mcp-sockets",  // only frontend-class servers, over per-session peer-checked unix sockets
	"in-unit-moxy",          // agent-side servers (moxy & co.) run inside the unit, moxy under InUnitMoxyfile
	"statsd-relay",          // in-unit UDP 127.0.0.1:8125 relayed over a peer-checked unixgram socket to the frontend's statsd
	"frontend-monitors",     // job-platform monitors run in the frontend; no ringmaster or troupe state is bound into the unit
	"frontend-serve-socket", // spinclass serve's bridge listens on a per-session unix socket admitting only the unit (Result.ServeSocket); no TCP listener
	"frontend-deadman",      // the unit stops when its frontend dies, by any signal (clown#319)
	"model-inject-proxy",    // the model API credential is injected by the frontend (InjectProxy); the unit holds a placeholder. Result.ModelCredential says which path a launch took
}

// resultCapabilities is what a unit of this launch reports: Capabilities, plus
// bridge-v1 when the bridge directory is bound.
func (l Launch) resultCapabilities() []string {
	if l.BridgeDir != "" && l.BridgeReady != nil && l.BridgeReady.Load() {
		return append(slices.Clone(Capabilities), "bridge-v1")
	}
	return Capabilities
}

// Result is what clown reports back once the unit is active (FDR 0020
// "Launcher interface"). Unit is authoritative. The shape lives in
// internal/*/unitresult so another module can write the same file.
type Result = unitresult.Result

// WriteResult writes r to path atomically (tmp + rename in the same dir), with
// this build's spec version and, unless r names its own, its capabilities.
func WriteResult(path string, r Result) error {
	r.Version = SpecVersion
	if r.Capabilities == nil { // a result from a Launch already names its own (bridge-v1)
		r.Capabilities = Capabilities
	}
	return unitresult.WriteResult(path, r)
}

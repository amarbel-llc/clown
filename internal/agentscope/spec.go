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
	"strings"
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
}

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
	// clown's state holds the confinement records (clown#318); a unit that
	// could write it could rewrite its own session's record.
	// Compared lexically and with symlinks resolved, so neither a bind nor
	// the state dir reached through a symlink slips past.
	clownState := filepath.Join(OperatorXDG(os.Environ(), s.OperatorHome, "XDG_STATE_HOME"), "clown")
	realState := ResolvePath(clownState)
	for _, p := range append([]string{s.Worktree}, s.BindsRW...) {
		real := ResolvePath(p)
		if exposesWhole(p, clownState) || within(p, clownState) || exposesWhole(real, realState) || within(real, realState) {
			return fmt.Errorf("worktree or binds_rw entry %q makes clown's state %q writable from inside the unit", p, clownState)
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
	for _, p := range s.DenyPaths {
		if err := requireBindable("deny_paths", p); err != nil {
			return err
		}
	}
	for k := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("env key %q is not a valid variable name", k)
		}
		if scrubbedKeys[k] {
			return fmt.Errorf("env key %q is scrubbed from the agent scope and may not be set by the spec", k)
		}
	}
	return nil
}

// ResolvePath is p with symlinks resolved: the deepest existing ancestor is
// resolved and the missing rest appended, so a path that does not exist yet
// still resolves through a symlinked parent. With nothing resolvable it is
// the cleaned absolute path.
func ResolvePath(p string) string {
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	var rest []string
	for dir := p; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		if dir == filepath.Dir(dir) {
			return p
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
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
}

// Result is what clown reports back once the unit is active (FDR 0020
// "Launcher interface"). Unit is authoritative.
type Result struct {
	Version      int      `json:"version"`
	Capabilities []string `json:"capabilities"`
	Unit         string   `json:"unit,omitempty"`
	// Principal is the session's CLOWN_SESSION_ID, reported by the frontend:
	// clown keeps it out of the claude subtree's environment (clown#136), so
	// it is not readable from /proc/<main_pid>/environ.
	Principal    string `json:"principal,omitempty"`
	Cgroup       string `json:"cgroup,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	UID          int    `json:"uid"`
	Pty          string `json:"pty,omitempty"`
	MainPID      int    `json:"main_pid,omitempty"`
	// ServeSocket is the session's serve socket (F12): the frontend unix
	// socket spinclass serve's bridge listens on, which admits only this unit
	// (and the frontend). Absent when the session has none.
	ServeSocket string `json:"serve_socket,omitempty"`
	// ExitCode is set once the unit has exited after its provider started.
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// WriteResult writes r to path atomically (tmp + rename in the same dir).
func WriteResult(path string, r Result) error {
	r.Version = SpecVersion
	r.Capabilities = Capabilities
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-unit-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

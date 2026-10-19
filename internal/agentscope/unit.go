package agentscope

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Slice is the systemd slice every agent unit runs in. Its cgroup prefix,
// .../user@<uid>.service/clown.slice/clown-agent.slice/, is what piggy, posh
// and spinclass match a caller against.
const Slice = "clown-agent.slice"

const defaultUnitHint = "clown-agent"

// Launch is everything needed to start one agent unit.
type Launch struct {
	Spec Spec
	// Unit is the full unit name, ".service" included (see UnitName).
	Unit string
	// WorkDir is the provider's working directory; it must be inside the
	// worktree, the only part of the home the unit can see.
	WorkDir string
	// AgentHome becomes HOME inside the unit.
	AgentHome string
	// FrontendEnv is clown's own environment ("K=V"), the source of the
	// allowlisted pass-through keys.
	FrontendEnv []string
	// ManagerEnvKeys are the keys the user manager injects into every unit
	// (systemctl --user show-environment); every one not set by clown is
	// unset.
	ManagerEnvKeys []string
	// ExtraEnv is the provider binding's own "K=V" entries.
	ExtraEnv []string
	// PlumbingRW and PlumbingRO are clown's own binds (job-platform state,
	// claude config). Missing paths are skipped rather than failing the start.
	PlumbingRW []string
	PlumbingRO []string
	// Shim is the in-unit exec shim, e.g. [clown, agent-exec]; the launcher
	// appends its egress-relay flag and "--".
	Shim []string
	// Argv is the provider binary and its arguments.
	Argv []string
	// EgressSocket is the frontend egress proxy's unix socket, bound into
	// the unit as a single inode.
	EgressSocket string
	// Forwards are the loopback services the unit may reach: each in-unit
	// listen address relays to a frontend PortForward socket, bound in as a
	// single inode.
	Forwards []Relay
}

// relays is every in-unit relay the shim starts: the egress proxy's, then the
// forwards.
func (l Launch) relays() []Relay {
	return append([]Relay{{Listen: RelayAddr(), Socket: l.EgressSocket}}, l.Forwards...)
}

// UnitName turns spinclass's hint into a valid, unique service name.
func UnitName(hint string) string {
	var b strings.Builder
	for _, r := range hint {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-', r == ':':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-.")
	if name == "" {
		name = defaultUnitHint
	}
	if len(name) > 200 {
		name = name[:200]
	}
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	return name + "-" + hex.EncodeToString(suffix[:]) + ".service"
}

// SystemdRunArgs renders the systemd-run argv (without argv[0]).
func (l Launch) SystemdRunArgs() ([]string, error) {
	if !strings.HasSuffix(l.Unit, ".service") {
		return nil, fmt.Errorf("unit %q must be a .service", l.Unit)
	}
	if !within(l.WorkDir, l.Spec.Worktree) {
		return nil, fmt.Errorf("working directory %q is outside the worktree %q", l.WorkDir, l.Spec.Worktree)
	}
	if len(l.Argv) == 0 {
		return nil, fmt.Errorf("empty provider argv")
	}
	if l.EgressSocket == "" {
		return nil, fmt.Errorf("no egress proxy socket: a unit without its proxy is not offered")
	}
	args := []string{
		"--user", "--quiet", "--collect", "--wait", "--pipe",
		"--expand-environment=no",
		"--slice=" + Slice,
		"--unit=" + l.Unit,
	}
	for _, p := range l.properties() {
		args = append(args, "--property="+p)
	}
	set, unset := l.environment()
	if len(unset) > 0 {
		args = append(args, "--property=UnsetEnvironment="+strings.Join(unset, " "))
	}
	for _, kv := range set {
		args = append(args, "--setenv="+kv)
	}
	args = append(args, "--")
	args = append(args, l.Shim...)
	for _, r := range l.relays() {
		args = append(args, "--relay", r.String())
	}
	args = append(args, "--")
	args = append(args, l.Argv...)
	return args, nil
}

// properties is the sandbox, in the order FDR 0020 tabulates it.
func (l Launch) properties() []string {
	props := []string{
		"PrivateUsers=self",
		"PrivatePIDs=yes",
		"ProtectHome=tmpfs",
		"TemporaryFileSystem=/dev/pts",
		"PrivateTmp=yes",
		"ProtectSystem=strict",
		"NoNewPrivileges=yes",
		"WorkingDirectory=" + l.WorkDir,
		"PrivateNetwork=yes",
		"BindPaths=" + l.EgressSocket,
	}
	props = append(props, "BindPaths="+l.Spec.Worktree, "ReadWritePaths="+l.Spec.Worktree)
	for _, p := range l.Spec.BindsRW {
		props = append(props, "BindPaths="+p)
	}
	for _, s := range l.Spec.Sockets {
		props = append(props, "BindPaths="+s.Path)
	}
	for _, f := range l.Forwards {
		props = append(props, "BindPaths="+f.Socket)
	}
	for _, p := range l.PlumbingRW {
		props = append(props, "BindPaths=-"+p)
	}
	for _, p := range l.Spec.BindsRO {
		props = append(props, "BindReadOnlyPaths="+p)
	}
	for _, p := range l.PlumbingRO {
		props = append(props, "BindReadOnlyPaths=-"+p)
	}
	for _, p := range l.Spec.DenyPaths {
		props = append(props, "InaccessiblePaths=-"+p)
	}
	return props
}

func within(p, dir string) bool {
	p = filepath.Clean(p)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// passKeys are copied verbatim from the frontend when set there.
var passKeys = map[string]bool{
	"PATH": true, "TERM": true, "COLORTERM": true, "TERMINFO": true, "TERMINFO_DIRS": true,
	"LANG": true, "TZ": true, "TZDIR": true, "LOCALE_ARCHIVE": true,
	"USER": true, "LOGNAME": true, "SHELL": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "CURL_CA_BUNDLE": true, "GIT_SSL_CAINFO": true,
	"XDG_RUNTIME_DIR": true,
	"CLOWN_BIN":       true, "CLOWN_NAME": true, "CLOWN_GROUP_ID": true, "CLOWN_GROUP_DESCRIPTION": true, "CLOWN_XMPP_VHOST": true,
}

// passPrefixes are copied verbatim from the frontend when set there.
var passPrefixes = []string{"LC_", "NIX_", "ANTHROPIC_", "CLAUDE_CODE_", "TROUPE_"}

// tmpDirKeys name temp directories; they pass only when inside the worktree.
var tmpDirKeys = map[string]bool{"TMPDIR": true, "CLAUDE_CODE_TMPDIR": true}

// scrubbedKeys never reach the unit from the frontend and the spec may not
// set them. XDG_RUNTIME_DIR is NOT here: it is passed so job-platform tools
// agree with the frontend on paths, and under ProtectHome=tmpfs it holds only
// what was bound in.
var scrubbedKeys = map[string]bool{
	"DBUS_SESSION_BUS_ADDRESS": true, "DISPLAY": true, "WAYLAND_DISPLAY": true, "XAUTHORITY": true,
	"CLOWN_SESSION_ID": true, "HOME": true, "SSH_AUTH_SOCK": true,
}

// environment returns the "K=V" entries to set (sorted) and the manager keys
// to unset.
func (l Launch) environment() (set, unset []string) {
	env := map[string]string{}
	for _, kv := range l.FrontendEnv {
		k, v, ok := strings.Cut(kv, "=")
		switch {
		case !ok:
		case k == "SSH_AUTH_SOCK":
			// Passes only when it names a socket the spec binds in.
			if l.bindsSocket(v) {
				env[k] = v
			}
		case tmpDirKeys[k]:
			// Outside the worktree a temp dir is read-only or absent inside
			// the unit (claude then fails with EROFS), so it is redirected.
			if within(v, l.Spec.Worktree) {
				env[k] = v
			} else {
				env[k] = filepath.Join(l.Spec.Worktree, ".tmp")
			}
		case scrubbedKeys[k]:
		case passKeys[k] || hasAnyPrefix(k, passPrefixes):
			env[k] = v
		}
	}
	for _, kv := range l.ExtraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && !scrubbedKeys[k] {
			env[k] = v
		}
	}
	for k, v := range l.Spec.Env {
		env[k] = v
	}
	env["HOME"] = l.AgentHome
	// State and log dirs point at the operator's paths, where clown binds the
	// job platform's and the hooks' subdirs, so tools inside agree with the
	// frontend. Config, cache and data stay unset: they default under the
	// writable agent home rather than the hidden, read-only real one.
	env["XDG_STATE_HOME"] = OperatorXDG(l.FrontendEnv, l.Spec.OperatorHome, "XDG_STATE_HOME")
	env["XDG_LOG_HOME"] = OperatorXDG(l.FrontendEnv, l.Spec.OperatorHome, "XDG_LOG_HOME")
	// The unit's only way out (PrivateNetwork) is the in-unit relay.
	proxy := "http://" + RelayAddr()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		env[k] = proxy
	}
	env["NO_PROXY"] = "localhost,127.0.0.1,::1"
	env["no_proxy"] = env["NO_PROXY"]
	// -F none: under PrivateUsers root-owned files read as nobody-owned, and
	// ssh refuses the system config (systemd's ssh_config.d) as badly owned.
	// The operator's ~/.ssh is hidden anyway.
	if len(l.Shim) > 0 {
		env["GIT_SSH_COMMAND"] = fmt.Sprintf("ssh -F none -o ProxyCommand=%q", l.Shim[0]+" egress-connect "+l.EgressSocket+" %h %p")
	}
	for k, v := range env {
		set = append(set, k+"="+v)
	}
	sort.Strings(set)
	for _, k := range l.ManagerEnvKeys {
		if _, ok := env[k]; !ok {
			unset = append(unset, k)
		}
	}
	sort.Strings(unset)
	return set, unset
}

// RelayAddr is the in-unit relay's listen address.
func RelayAddr() string { return fmt.Sprintf("127.0.0.1:%d", RelayPort) }

var xdgDefaults = map[string]string{
	"XDG_STATE_HOME": ".local/state",
	"XDG_LOG_HOME":   ".local/log",
}

// OperatorXDG resolves an XDG base dir as the frontend sees it: its value in
// env when absolute, else the spec's default under the operator's home.
func OperatorXDG(env []string, operatorHome, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key && filepath.IsAbs(v) {
			return v
		}
	}
	return filepath.Join(operatorHome, xdgDefaults[key])
}

func (l Launch) bindsSocket(path string) bool {
	for _, s := range l.Spec.Sockets {
		if s.Path == path {
			return true
		}
	}
	return false
}

func hasAnyPrefix(k string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

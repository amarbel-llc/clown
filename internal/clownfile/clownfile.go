// Package clownfile reads clown's cascading per-instance config file (RFC-0013
// §1): a `clownfile` (TOML) discovered by ascending from $PWD to $HOME, with a
// deeper file overriding a shallower one per key. It implements the [profile]
// layer (provider/backend/model/env defaults, §1.2) and the [attach] layer
// (multiplexer self-wrap templates, §1.3).
package clownfile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/amarbel-llc/clown/internal/promptwalk"
)

// Filename is the per-instance config file clown discovers by ascending from
// $PWD to $HOME (RFC-0013 §1.1).
const Filename = "clownfile"

// XDGPath returns the user-global clownfile under the XDG config directory:
// $XDG_CONFIG_HOME/clown/clownfile, or <homeDir>/.config/clown/clownfile when
// $XDG_CONFIG_HOME is unset or empty (clown#149). It is the conventional
// per-user config location, keeping $HOME uncluttered. An empty homeDir with no
// $XDG_CONFIG_HOME yields "" (Discover then skips the layer). The path is not
// required to exist; Discover treats an absent file as non-fatal.
func XDGPath(homeDir string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if homeDir == "" {
			return ""
		}
		base = filepath.Join(homeDir, ".config")
	}
	return filepath.Join(base, "clown", Filename)
}

// Profile is the clownfile [profile] table: per-directory defaults for the run
// (RFC-0013 §1.2). It is a default layer beneath explicit flags/env; the
// named-profile registry (`--profile`) is a separate mechanism.
type Profile struct {
	Provider string            `toml:"provider"`
	Backend  string            `toml:"backend"`
	Model    string            `toml:"model"`
	Env      map[string]string `toml:"env"`
}

// Attach is the clownfile [attach] table: the clown-owned multiplexer/attach
// layer (RFC-0013 §1.3) that subsumes spinclass's former [session-entry] mux
// templates. clown EXECUTES Start/Resume (self-wrap on boot); Spawn/SpawnEntry/
// SpawnWindow are parsed and held as the single-source schema but their executor
// is unresolved (RFC §1.3 open question), so this revision does not run them.
type Attach struct {
	Multiplexer string   `toml:"multiplexer"`  // "zmx" | "posh" | "none"
	GroupID     string   `toml:"group-id"`     // group key, env-interpolated (RFC-0014 §2); "" ⇒ ungrouped
	Description string   `toml:"description"`  // presence label, env-interpolated (RFC-0014 §4.1)
	Start       []string `toml:"start"`        // fresh interactive self-wrap argv
	Resume      []string `toml:"resume"`       // reattach argv
	ResumeTitle string   `toml:"resume-title"` // OSC-2 title emitted before a resume attach
	Spawn       []string `toml:"spawn"`        // detached-worker launch (RFC-0014 §5)
	SpawnEntry  []string `toml:"spawn-entry"`  // harness argv a spawned worker boots (schema-only)
	SpawnWindow []string `toml:"spawn-window"` // fire-and-forget window opener (schema-only)
	// PtySuspend opts the interactive provider run into the escape-to-shell pty
	// proxy (internal/ptysuspend): clown runs the provider on an inner pty and
	// intercepts the escape key (EscapeKey) before the raw-mode TUI swallows it,
	// handing the terminal to EscapeCommand and resuming on its exit. A *bool so a
	// deeper clownfile can override an enabled default back to false (a plain
	// bool's false zero value would not override). nil = unset = off. See
	// PtySuspendEnabled.
	PtySuspend *bool `toml:"pty-suspend"`
	// EscapeKey names the input key that triggers the escape, as "^X" (caret
	// notation, e.g. "^Z"). Empty defaults to ^Z. Parsed by the caller.
	EscapeKey string `toml:"escape-key"`
	// EscapeCommand is the argv run (with the terminal handed to it) on the escape
	// key, each element env-interpolated by the caller. Empty falls back to the
	// user's $SHELL in the worktree. The intended default is
	// ["sc", "exec", "${SPINCLASS_SESSION_ID}", "$SHELL"].
	EscapeCommand []string `toml:"escape-command"`
}

// ResolveEnv expands ${NAME} / $NAME environment references in a config string,
// substituting an unset variable with the empty string (RFC-0014 §2.1). It is
// applied to the env-interpolated fields (GroupID, Description); clown performs
// purely textual substitution and never interprets the meaning of a variable,
// which is what keeps clown orchestrator-agnostic.
func ResolveEnv(s string) string { return os.ExpandEnv(s) }

// AttachMode selects which executed template Resolve renders.
type AttachMode int

const (
	// ModeStart is a fresh interactive launch (the Start template).
	ModeStart AttachMode = iota
	// ModeResume is a reattach (the Resume template).
	ModeResume
	// ModeSpawn is a detached-worker launch (the Spawn template, RFC-0014 §5).
	ModeSpawn
)

// Enabled reports whether [attach] requests a multiplexer wrap. "" and "none"
// mean run inline (RFC-0013 §1.3 rule 1).
func (a Attach) Enabled() bool {
	return a.Multiplexer != "" && a.Multiplexer != "none"
}

// PtySuspendEnabled reports whether the ctrl-z escape-to-shell pty proxy is
// requested. Unset (nil) is off.
func (a Attach) PtySuspendEnabled() bool {
	return a.PtySuspend != nil && *a.PtySuspend
}

// placeholderRe matches a {placeholder} token so Resolve can reject any that
// survives substitution (RFC-0013 §1.3 rule 2).
var placeholderRe = regexp.MustCompile(`\{[a-zA-Z][a-zA-Z0-9_-]*\}`)

// Resolve renders the executed template for mode (Start/Resume/Spawn) into a
// concrete multiplexer argv (RFC-0013 §1.3, RFC-0014 §5): the exact element
// "{entry}" is replaced by splicing the entry argv into that position, and
// "{id}" is string-substituted within any element. Only {id}/{entry} are
// available in these argv templates; any other surviving {...} placeholder (e.g.
// {group} — which is title-only — {prompt}/{dir}, or an unknown token) is
// rejected with a diagnostic. Returns an error when the multiplexer is not
// enabled or the selected template is empty.
func (a Attach) Resolve(mode AttachMode, id string, entry []string) ([]string, error) {
	if a.Multiplexer != "zmx" && a.Multiplexer != "posh" && a.Multiplexer != "none" {
		return nil, fmt.Errorf("clownfile [attach]: multiplexer must be \"zmx\", \"posh\", or \"none\", got %q", a.Multiplexer)
	}
	if !a.Enabled() {
		return nil, fmt.Errorf("clownfile [attach]: multiplexer is %q; no wrap", a.Multiplexer)
	}
	var tmpl []string
	switch mode {
	case ModeStart:
		tmpl = a.Start
	case ModeResume:
		tmpl = a.Resume
	case ModeSpawn:
		tmpl = a.Spawn
	default:
		return nil, fmt.Errorf("clownfile [attach]: unknown mode %d", mode)
	}
	if len(tmpl) == 0 {
		return nil, fmt.Errorf("clownfile [attach]: empty template for the requested mode")
	}
	out := make([]string, 0, len(tmpl)+len(entry))
	for _, el := range tmpl {
		if el == "{entry}" {
			out = append(out, entry...)
			continue
		}
		s := strings.ReplaceAll(el, "{id}", id)
		if m := placeholderRe.FindString(s); m != "" {
			return nil, fmt.Errorf("clownfile [attach]: unrecognized or unavailable placeholder %s in %q", m, el)
		}
		out = append(out, s)
	}
	return out, nil
}

// Title renders ResumeTitle for emission as an OSC-2 terminal title before a
// resume attach (RFC-0013 §1.3 rule 4, RFC-0014 §3.1). {group} resolves to the
// group key, falling back to the per-instance id when the group is empty (so a
// bare clown shows its key, never a literal "{group}"); {id} resolves to the
// per-instance id. Empty when no title is configured.
func (a Attach) Title(id, group string) string {
	g := group
	if g == "" {
		g = id
	}
	t := strings.ReplaceAll(a.ResumeTitle, "{group}", g)
	return strings.ReplaceAll(t, "{id}", id)
}

// Messaging is the clownfile [messaging] table: clown's opt-in for troupe's
// XMPP messaging transport (troupe RFC-0001 §8). It cascades like
// [profile]/[attach]. When Transport is "xmpp", clown resolves it (via Env)
// into the TROUPE_* environment troupe's `agent` + `mcp` children read
// (RFC-0001 §1) and injects that env into those children. The default (local)
// emits no TROUPE_XMPP_* vars, so the run stays on the local journal
// single-host — the transport is inert until a clownfile opts in.
type Messaging struct {
	Transport        string `toml:"transport"`         // "local" (default) | "xmpp"
	XMPPHost         string `toml:"xmpp-host"`         // connect addr, env-interpolated; troupe defaults to DNS-resolving xmpp-domain
	XMPPPort         int    `toml:"xmpp-port"`         // c2s port; troupe defaults to 5222
	XMPPDomain       string `toml:"xmpp-domain"`       // c2s vhost; REQUIRED when transport=xmpp
	XMPPMUCDomain    string `toml:"xmpp-muc-domain"`   // base MUC domain rooms are addressed under; REQUIRED when transport=xmpp
	XMPPNick         string `toml:"xmpp-nick"`         // MUC nick; troupe defaults to the clown-name
	XMPPInsecure     *bool  `toml:"xmpp-insecure"`     // skip TLS verify (loopback/testing only). *bool so a deeper clownfile can override an enabled default
	XMPPUser         string `toml:"xmpp-user"`         // authenticated-mode c2s localpart; used only when xmpp-password-file is set (troupe defaults it to the nick)
	XMPPPasswordFile string `toml:"xmpp-password-file"` // credential REFERENCE: a file path (env-interpolated), NEVER the secret. Only-if-authenticated.
}

// Env resolves the [messaging] table into the TROUPE_* environment for troupe's
// `agent` + `mcp` children (troupe RFC-0001 §1/§8). Transport "" or "local"
// returns an empty map (the default: local journal, no XMPP vars emitted).
// Transport "xmpp" emits TROUPE_TRANSPORT=xmpp plus the coordinate vars, and
// FAILS FAST (error) when a required var (xmpp-domain, xmpp-muc-domain) is
// missing — so the diagnostic lands at clown's config layer, not opaquely
// inside the troupe child. Optional vars are emitted only when set. The
// credential is emitted BY REFERENCE (the file path only, env-interpolated) —
// never inline — satisfying §1's credential-by-reference MUST. Any transport
// other than ""/"local"/"xmpp" is an error.
func (m Messaging) Env() (map[string]string, error) {
	switch m.Transport {
	case "", "local":
		return map[string]string{}, nil
	case "xmpp":
		// resolved below
	default:
		return nil, fmt.Errorf("clownfile [messaging]: transport must be \"local\" or \"xmpp\", got %q", m.Transport)
	}
	if m.XMPPDomain == "" || m.XMPPMUCDomain == "" {
		return nil, fmt.Errorf("clownfile [messaging]: transport=xmpp requires xmpp-domain and xmpp-muc-domain")
	}
	env := map[string]string{
		"TROUPE_TRANSPORT":       "xmpp",
		"TROUPE_XMPP_DOMAIN":     m.XMPPDomain,
		"TROUPE_XMPP_MUC_DOMAIN": m.XMPPMUCDomain,
	}
	if m.XMPPHost != "" {
		env["TROUPE_XMPP_HOST"] = ResolveEnv(m.XMPPHost)
	}
	if m.XMPPPort != 0 {
		env["TROUPE_XMPP_PORT"] = strconv.Itoa(m.XMPPPort)
	}
	if m.XMPPNick != "" {
		env["TROUPE_XMPP_NICK"] = m.XMPPNick
	}
	if m.XMPPInsecure != nil && *m.XMPPInsecure {
		env["TROUPE_XMPP_INSECURE"] = "1"
	}
	if m.XMPPUser != "" {
		env["TROUPE_XMPP_USER"] = m.XMPPUser
	}
	if m.XMPPPasswordFile != "" {
		// Credential by reference: emit the PATH only, env-interpolated so a
		// runtime-provisioned secret path (e.g. ${XDG_RUNTIME_DIR}/troupe.pass)
		// resolves at launch and never sits in the committed clownfile.
		env["TROUPE_XMPP_PASSWORD_FILE"] = ResolveEnv(m.XMPPPasswordFile)
	}
	return env, nil
}

// Clownfile is one parsed clownfile.
type Clownfile struct {
	Profile   Profile   `toml:"profile"`
	Attach    Attach    `toml:"attach"`
	Messaging Messaging `toml:"messaging"`
}

// Load parses a single clownfile from disk.
func Load(path string) (Clownfile, error) {
	var c Clownfile
	_, err := toml.DecodeFile(path, &c)
	return c, err
}

// Discover assembles the clownfile cascade for startDir (RFC-0013 §1.1),
// merging lowest-to-highest precedence so a higher layer overrides per key:
//
//  1. basePath — the burned-in default clownfile (nix-store-shipped, §1.3);
//     lowest precedence. Absent is non-fatal (dev builds leave it ""), a
//     present-but-malformed one is an error.
//  2. xdgPath — the user-global clownfile under the XDG config dir
//     (typically from XDGPath(homeDir), i.e. ~/.config/clown/clownfile;
//     clown#149). Absent is non-fatal, malformed is an error.
//  3. the $PWD→$HOME ancestor chain, shallowest-first, so a deeper directory
//     clownfile (and $HOME/clownfile itself) overrides the XDG file.
//
// Absent everywhere yields the zero value (non-fatal).
func Discover(startDir, homeDir, basePath, xdgPath string) (Clownfile, error) {
	var merged Clownfile
	// 1. burned-in default (lowest precedence).
	if err := mergeFileIfPresent(&merged, basePath, "default clownfile"); err != nil {
		return Clownfile{}, err
	}
	// 2. XDG user-global clownfile, above the default but below the ascent.
	if err := mergeFileIfPresent(&merged, xdgPath, "xdg clownfile"); err != nil {
		return Clownfile{}, err
	}
	// 3. $PWD→$HOME ancestor chain (deeper overrides; $HOME/clownfile overrides XDG).
	ancestors, err := promptwalk.Ancestors(startDir, homeDir)
	if err != nil {
		return Clownfile{}, err
	}
	// ancestors is deepest-first; apply shallowest-first so deeper overrides.
	for i := len(ancestors) - 1; i >= 0; i-- {
		path := filepath.Join(ancestors[i], Filename)
		if _, err := os.Stat(path); err != nil {
			continue // absent at this level
		}
		c, err := Load(path)
		if err != nil {
			return Clownfile{}, fmt.Errorf("clownfile %s: %w", path, err)
		}
		mergeInto(&merged, c)
	}
	return merged, nil
}

// mergeFileIfPresent layers the clownfile at path over merged. An empty path or
// an absent file is a non-fatal skip; a present-but-malformed file is an error
// tagged with label. Shared by the burned-in default and XDG layers in Discover.
func mergeFileIfPresent(merged *Clownfile, path, label string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil // absent (or unstattable): non-fatal, skip the layer.
	}
	c, err := Load(path)
	if err != nil {
		return fmt.Errorf("%s %s: %w", label, path, err)
	}
	mergeInto(merged, c)
	return nil
}

// mergeInto layers src over dst: scalar fields replace when non-empty; Env keys
// union with src winning.
func mergeInto(dst *Clownfile, src Clownfile) {
	if src.Profile.Provider != "" {
		dst.Profile.Provider = src.Profile.Provider
	}
	if src.Profile.Backend != "" {
		dst.Profile.Backend = src.Profile.Backend
	}
	if src.Profile.Model != "" {
		dst.Profile.Model = src.Profile.Model
	}
	for k, v := range src.Profile.Env {
		if dst.Profile.Env == nil {
			dst.Profile.Env = map[string]string{}
		}
		dst.Profile.Env[k] = v
	}

	// [attach]: scalars replace when non-empty; argv templates replace wholesale
	// when set (a deeper clownfile's template wins as a unit — argv lists do not
	// merge element-wise).
	if src.Attach.Multiplexer != "" {
		dst.Attach.Multiplexer = src.Attach.Multiplexer
	}
	if src.Attach.GroupID != "" {
		dst.Attach.GroupID = src.Attach.GroupID
	}
	if src.Attach.Description != "" {
		dst.Attach.Description = src.Attach.Description
	}
	if src.Attach.ResumeTitle != "" {
		dst.Attach.ResumeTitle = src.Attach.ResumeTitle
	}
	if src.Attach.Start != nil {
		dst.Attach.Start = src.Attach.Start
	}
	if src.Attach.Resume != nil {
		dst.Attach.Resume = src.Attach.Resume
	}
	if src.Attach.Spawn != nil {
		dst.Attach.Spawn = src.Attach.Spawn
	}
	if src.Attach.SpawnEntry != nil {
		dst.Attach.SpawnEntry = src.Attach.SpawnEntry
	}
	if src.Attach.SpawnWindow != nil {
		dst.Attach.SpawnWindow = src.Attach.SpawnWindow
	}
	// *bool: a set value (true or false) in a deeper file overrides; nil inherits.
	if src.Attach.PtySuspend != nil {
		dst.Attach.PtySuspend = src.Attach.PtySuspend
	}
	if src.Attach.EscapeKey != "" {
		dst.Attach.EscapeKey = src.Attach.EscapeKey
	}
	if src.Attach.EscapeCommand != nil {
		dst.Attach.EscapeCommand = src.Attach.EscapeCommand
	}

	// [messaging]: scalars replace when non-empty; *bool overrides when set;
	// int replaces when non-zero.
	if src.Messaging.Transport != "" {
		dst.Messaging.Transport = src.Messaging.Transport
	}
	if src.Messaging.XMPPHost != "" {
		dst.Messaging.XMPPHost = src.Messaging.XMPPHost
	}
	if src.Messaging.XMPPPort != 0 {
		dst.Messaging.XMPPPort = src.Messaging.XMPPPort
	}
	if src.Messaging.XMPPDomain != "" {
		dst.Messaging.XMPPDomain = src.Messaging.XMPPDomain
	}
	if src.Messaging.XMPPMUCDomain != "" {
		dst.Messaging.XMPPMUCDomain = src.Messaging.XMPPMUCDomain
	}
	if src.Messaging.XMPPNick != "" {
		dst.Messaging.XMPPNick = src.Messaging.XMPPNick
	}
	if src.Messaging.XMPPInsecure != nil {
		dst.Messaging.XMPPInsecure = src.Messaging.XMPPInsecure
	}
	if src.Messaging.XMPPUser != "" {
		dst.Messaging.XMPPUser = src.Messaging.XMPPUser
	}
	if src.Messaging.XMPPPasswordFile != "" {
		dst.Messaging.XMPPPasswordFile = src.Messaging.XMPPPasswordFile
	}
}

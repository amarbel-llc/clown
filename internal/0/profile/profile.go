package profile

import (
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Profile struct {
	Name     string            `toml:"name"`
	Display  string            `toml:"display"`
	Provider string            `toml:"provider"`
	Backend  string            `toml:"backend"`
	Model    string            `toml:"model"`
	URL      string            `toml:"url,omitempty"`
	Token    string            `toml:"token,omitempty"`
	Env      map[string]string `toml:"env,omitempty"`

	// ContextServers and ContextExcluded are a saved --cheap-context
	// selection (cmd/clown/cheapcontext.go): ContextServers is the exact
	// set of MCP server names to keep — everything else discovered at
	// launch is dropped, mirroring the picker's whole-server semantics —
	// and ContextExcluded, keyed by a kept server's name, lists the tool
	// names to additionally exclude from that server, mirroring the
	// picker's per-tool semantics. Both are nil on a profile with no saved
	// selection (omitempty keeps such profiles free of empty TOML arrays).
	//
	// A server or tool name here that no longer exists in a later launch's
	// discovered/fetched catalog is skipped when applying the selection: the
	// live catalog is the source of truth. Two cases refuse the launch
	// instead. When none of ContextServers is started or kept, the launch
	// does not fall back to the original plugin dirs, which would run every
	// server un-narrowed (cmd/clown runUnbound). When none of a kept
	// server's ContextExcluded names matches a live tool (an all-stale list,
	// say after a rename), the tools it meant to hide may now be visible
	// under new names (cmd/clown exclusionreadback, clown#304).
	ContextServers  []string            `toml:"context_servers,omitempty"`
	ContextExcluded map[string][]string `toml:"context_excluded,omitempty"`

	// Base names the builtin this (user) profile claims to narrow, as
	// "<builtin>" or "<builtin>@sha256:<hex>" pinning the builtin's
	// canonical digest (pkg/profilelookup). It is a declaration, not
	// inheritance: nothing is copied from the base. A launcher (spinclass)
	// decides whether the profile really narrows it; clown resolves the base
	// among the builtins and refuses a pin that no longer matches. Protocol 2.
	Base string `toml:"base,omitempty"`

	// The moxy keys describe the profile's moxy under confinement (V3
	// structural moxy, clown#339; protocol 3): where it runs and the tool
	// ceiling the unit mounts read-only. They are flat keys on the profile
	// (moxy_placement, moxy_allow_tools, moxy_tools) because the in-place
	// TOML encoder writes primitives, lists and map tables but not a nested
	// struct table; the canonical form renders them as one `moxy` object.
	// All three unset means the profile says nothing about it, which
	// spinclass reads as today's rule (a confined spawn may not keep moxy).
	// MoxyTable gives the typed view.
	MoxyPlacement  string   `toml:"moxy_placement,omitempty"`
	MoxyAllowTools []string `toml:"moxy_allow_tools,omitempty"`
	MoxyTools      []string `toml:"moxy_tools,omitempty"`
}

// Moxy is the typed view of a profile's moxy keys (protocol 3). Placement is
// where the profile's moxy runs: "in-unit" (inside the agent unit under the
// ceiling), "frontend" (the operator's moxy, never for a confined spawn) or
// "twin" (reserved; refused by launchers until specified). AllowTools is the
// ceiling, the allow-tools list of the read-only global moxyfile the unit
// mounts, and Tools the effective set after the ceiling and the profile's
// exclusions, both as the agent sees the names (mcp__<server>__<tool>, e.g.
// mcp__plugin_moxy_moxy__folio_read).
// Both ride so a launcher can check Tools ⊆ AllowTools without a catalog:
// clown computes neither from a live moxy, they are definition data.
type Moxy struct {
	Placement  string
	AllowTools []string
	Tools      []string
}

// MoxyTable returns the profile's moxy keys as one value, or nil when none
// of them is set.
func (p Profile) MoxyTable() *Moxy {
	if p.MoxyPlacement == "" && p.MoxyAllowTools == nil && p.MoxyTools == nil {
		return nil
	}
	return &Moxy{Placement: p.MoxyPlacement, AllowTools: p.MoxyAllowTools, Tools: p.MoxyTools}
}

// SetMoxy writes m onto the profile's moxy keys (nil clears them).
func (p *Profile) SetMoxy(m *Moxy) {
	if m == nil {
		p.MoxyPlacement, p.MoxyAllowTools, p.MoxyTools = "", nil, nil
		return
	}
	p.MoxyPlacement, p.MoxyAllowTools, p.MoxyTools = m.Placement, m.AllowTools, m.Tools
}

// MoxyPlacements are the values Moxy.Placement may take.
var MoxyPlacements = []string{"in-unit", "frontend", "twin"}

// ProtocolVersion versions what a registry writer and a registry reader must
// agree on: the profiles.toml schema and the canonical-JSON digest form
// (pkg/profilelookup). Bump it on any change to either — a reader refuses a
// registry stamped by a newer protocol instead of mis-resolving it.
//
// Protocol 2 added `base` and renders env values as digests in the canonical
// form. Protocol 3 added the `[profile.moxy]` table (announced to spinclass
// 2026-11-03). A reader still reads every older protocol (each is a strict
// subset), and Save stamps the lowest protocol that holds the file, so an
// untouched registry stays readable by older clowns.
const ProtocolVersion = 3

// OldestReadableProtocol is the oldest registry protocol this build reads.
const OldestReadableProtocol = 1

// ReadableProtocol reports whether a registry stamped with protocol p can be
// read in full by this build.
func ReadableProtocol(p int) bool {
	return p >= OldestReadableProtocol && p <= ProtocolVersion
}

// protocolOf is the lowest protocol that holds one profile: 3 when it has
// moxy keys, 2 when it uses `base`, else 1.
func protocolOf(p Profile) int {
	switch {
	case p.MoxyTable() != nil:
		return 3
	case p.Base != "":
		return 2
	}
	return 1
}

// protocolFor is the lowest protocol that holds every profile.
func protocolFor(profiles []Profile) int {
	n := 1
	for _, p := range profiles {
		n = max(n, protocolOf(p))
	}
	return n
}

// ParseBase splits a `base` value into the builtin's name and the pinned
// lowercase-hex sha256 ("" when unpinned).
func ParseBase(base string) (name, pin string, err error) {
	name, rest, pinned := strings.Cut(base, "@")
	if name == "" || strings.TrimSpace(name) != name {
		return "", "", fmt.Errorf("base %q: no builtin name", base)
	}
	if !pinned {
		return name, "", nil
	}
	pin, ok := strings.CutPrefix(rest, "sha256:")
	if !ok || len(pin) != 64 || strings.Trim(pin, "0123456789abcdef") != "" {
		return "", "", fmt.Errorf("base %q: a pin is sha256: followed by 64 lowercase hex digits", base)
	}
	return name, pin, nil
}

// RegistryStamp is the writer stamp Save leaves in the `[registry]` table of
// profiles.toml: which clown last wrote the file and under which protocol.
// The zero value means the file predates stamping (or was never written by
// clown).
type RegistryStamp struct {
	ClownVersion string `toml:"clown_version"`
	Protocol     int    `toml:"protocol"`
}

// file is the profiles.toml shape. The stamp rides as a string map (the
// `[registry]` table) rather than a struct because tommy's in-place encoder
// can create a missing map table but not a missing struct table, and the
// map path appends the table after the [[profile]] entries.
type file struct {
	Registry map[string]string `toml:"registry,omitempty"`
	Profile  []Profile         `toml:"profile"`
}

func (s RegistryStamp) toMap() map[string]string {
	return map[string]string{
		"clown_version": s.ClownVersion,
		"protocol":      strconv.Itoa(s.Protocol),
	}
}

// stampFromMap reads a `[registry]` table. A protocol that is not an integer
// reads as -1, which matches no real protocol, so it surfaces as skew.
func stampFromMap(m map[string]string) RegistryStamp {
	if len(m) == 0 {
		return RegistryStamp{}
	}
	s := RegistryStamp{ClownVersion: m["clown_version"]}
	if raw, ok := m["protocol"]; ok {
		n, err := strconv.Atoi(raw)
		if err != nil {
			n = -1
		}
		s.Protocol = n
	}
	return s
}

//go:embed builtin.toml
var builtinTOML []byte

// Builtin returns the profiles compiled into clown (builtin.toml). The user
// registry layers on top of these via Merge.
func Builtin() ([]Profile, error) { return parseBuiltins(builtinTOML) }

// parseBuiltins parses a builtin profile set. Builtins are the roots a `base`
// names, so one carrying a base is refused.
func parseBuiltins(data []byte) ([]Profile, error) {
	var f file
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("builtin profiles: %w", err)
	}
	for _, p := range f.Profile {
		if p.Base != "" {
			return nil, fmt.Errorf("builtin profile %q has base %q; builtins are roots", p.Name, p.Base)
		}
	}
	return f.Profile, nil
}

func Load(path string) ([]Profile, error) {
	r, err := LoadRegistry(path)
	if err != nil {
		return nil, err
	}
	return r.Profiles, nil
}

// Registry is a parsed profiles.toml: its profiles, its writer stamp, and the
// keys present in the file that this build's schema does not know (written by
// a newer clown) — a skew signal independent of the stamp.
type Registry struct {
	Profiles  []Profile
	Stamp     RegistryStamp
	Undecoded []string
}

func LoadRegistry(path string) (Registry, error) {
	var f file
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		return Registry{}, fmt.Errorf("load profiles %s: %w", path, err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		unknown = append(unknown, k.String())
	}
	return Registry{Profiles: f.Profile, Stamp: stampFromMap(f.Registry), Undecoded: unknown}, nil
}

// MisstampedBase returns the names of profiles carrying `base` in a registry
// stamped older than protocol 2 (or unstamped): a writer that did not know
// what base means, so readers refuse the registry rather than guess.
func (r Registry) MisstampedBase() []string {
	if r.Stamp.Protocol >= 2 {
		return nil
	}
	var out []string
	for _, p := range r.Profiles {
		if p.Base != "" {
			out = append(out, p.Name)
		}
	}
	return out
}

// MisstampedMoxy returns the names of profiles carrying moxy keys in a
// registry stamped older than protocol 3 (or unstamped): a writer that did
// not know what they mean, so readers refuse the registry rather than guess.
func (r Registry) MisstampedMoxy() []string {
	if r.Stamp.Protocol >= 3 {
		return nil
	}
	var out []string
	for _, p := range r.Profiles {
		if p.MoxyTable() != nil {
			out = append(out, p.Name)
		}
	}
	return out
}

func Merge(builtin, additional []Profile) []Profile {
	index := make(map[string]int, len(builtin))
	result := make([]Profile, len(builtin))
	copy(result, builtin)
	for i, p := range builtin {
		index[p.Name] = i
	}
	for _, p := range additional {
		if i, ok := index[p.Name]; ok {
			result[i] = p
		} else {
			result = append(result, p)
		}
	}
	return result
}

var validCombos = map[string]map[string]bool{
	"claude":     {"anthropic": true, "gateway": true, "local": true},
	"opencode":   {"anthropic": true, "gateway": true, "local": true},
	"crush":      {"anthropic": true, "gateway": true, "local": true},
	"openrouter": {"gateway": true},
}

func Validate(p Profile) error {
	backends, ok := validCombos[p.Provider]
	if !ok {
		return fmt.Errorf("profile %q: unknown provider %q (valid: %s)", p.Name, p.Provider, strings.Join(Providers(), ", "))
	}
	if !backends[p.Backend] {
		return fmt.Errorf("profile %q: provider %q does not support backend %q", p.Name, p.Provider, p.Backend)
	}
	if p.Backend == "gateway" {
		switch p.Provider {
		case "claude":
			// Either an inline URL (with its required Token) or a Model that
			// resolves via juggler (cmd/clown's applyNamedProfile juggler-
			// fallback branch, only wired up for the claude family so far —
			// docs/plans/2026-07-07-juggler-model-registry-design.md)
			// satisfies the gateway backend — a juggler-resolved remote
			// model gets its token from the registry, not the profile.
			if p.URL == "" && p.Model == "" {
				return fmt.Errorf("profile %q: backend gateway requires url or model (model resolves via juggler)", p.Name)
			}
			if p.URL != "" && p.Token == "" {
				return fmt.Errorf("profile %q: backend gateway requires token when url is set", p.Name)
			}
		case "openrouter":
			// URL is hardcoded to OpenRouter's OpenAI-compatible endpoint by
			// cmd/clown's runOpencode dispatch (Phase B,
			// docs/plans/2026-07-24-openrouter-non-anthropic-design.md), not
			// stored on the profile — only Token (and Model) are required.
			if p.Token == "" {
				return fmt.Errorf("profile %q: backend gateway requires token", p.Name)
			}
		default:
			// opencode/crush have no juggler-resolution fallback yet
			// (cmd/clown's runOpencode/runCrush read prof.URL/prof.Token
			// directly for the gateway case) — keep requiring an inline
			// url+token unconditionally.
			if p.URL == "" {
				return fmt.Errorf("profile %q: backend gateway requires url", p.Name)
			}
			if p.Token == "" {
				return fmt.Errorf("profile %q: backend gateway requires token", p.Name)
			}
		}
	}
	// ContextServers of length zero (as opposed to nil) is a value that can
	// never round-trip through Save/Load: the "context_servers,omitempty"
	// tag makes BurntSushi/toml drop ANY zero-length slice on write, nil or
	// not, so it would come back as nil on the next Load — silently
	// indistinguishable from "no saved --cheap-context selection at all."
	// Reject it here, at the one place every caller that constructs or
	// mutates a Profile is expected to pass through, rather than requiring
	// each call site to remember this hazard individually.
	if p.ContextServers != nil && len(p.ContextServers) == 0 {
		return fmt.Errorf("profile %q: context_servers must be nil or non-empty, not an empty list (an empty saved selection can never be told apart from no saved selection)", p.Name)
	}
	if p.Base != "" {
		if _, _, err := ParseBase(p.Base); err != nil {
			return fmt.Errorf("profile %q: %w", p.Name, err)
		}
	}
	if m := p.MoxyTable(); m != nil {
		if err := m.validate(); err != nil {
			return fmt.Errorf("profile %q: moxy: %w", p.Name, err)
		}
	}
	return nil
}

// validate checks the moxy keys: a known placement, tool names in the shape
// the agent sees (mcp__<server>__<tool>), and Tools ⊆ AllowTools.
func (m Moxy) validate() error {
	if !slices.Contains(MoxyPlacements, m.Placement) {
		return fmt.Errorf("moxy_placement %q is not one of %s", m.Placement, strings.Join(MoxyPlacements, ", "))
	}
	allowed := map[string]bool{}
	for _, t := range m.AllowTools {
		if err := checkToolName(t); err != nil {
			return fmt.Errorf("allow_tools: %w", err)
		}
		allowed[t] = true
	}
	for _, t := range m.Tools {
		if err := checkToolName(t); err != nil {
			return fmt.Errorf("tools: %w", err)
		}
		if !allowed[t] {
			return fmt.Errorf("tools entry %q is not in allow_tools (the effective set may only narrow the ceiling)", t)
		}
	}
	return nil
}

// checkToolName accepts the agent-side name of an MCP tool,
// mcp__<server>__<tool> (the server key as claude renders it, e.g.
// plugin_moxy_moxy, then the tool, e.g. folio_read), each part non-empty and
// without whitespace.
func checkToolName(name string) error {
	rest, ok := strings.CutPrefix(name, "mcp__")
	parts := strings.Split(rest, "__")
	if !ok || len(parts) != 2 {
		return fmt.Errorf("tool name %q is not mcp__<server>__<tool>", name)
	}
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, " \t\n") {
			return fmt.Errorf("tool name %q has an empty or blank part", name)
		}
	}
	return nil
}

// Backends returns the valid backend names for provider, sorted, or nil for
// an unknown provider. The single source of the provider/backend matrix for
// UI code (the profile TUI's select options).
func Backends(provider string) []string {
	m, ok := validCombos[provider]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(m))
	for b := range m {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Providers returns the known provider names, sorted.
func Providers() []string {
	out := make([]string, 0, len(validCombos))
	for p := range validCombos {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

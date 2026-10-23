// Package profilelookup is clown's public, linkable view of the named-profile
// registry: it resolves `clown --profile NAME` the way the clown binary does
// (builtin profiles layered under the user's profiles.toml) and reports what
// a launcher such as spinclass needs to decide whether to spawn under it,
// without shelling out to clown or parsing profiles.toml itself (clown#264,
// spinclass FDR 0034).
//
// # Surface
//
//   - Resolve returns a Resolved: the profile's identity (never its token), its
//     Selection (the saved --cheap-context keep-set), its Env as per-value
//     digests, its Base (the builtin it declares it narrows, resolved the same
//     way), the canonical JSON of the definition, and the sha256 of those
//     bytes. A base that cannot be vouched for fails with *BaseError.
//   - RequiredServers is the set of MCP servers a worker needs to reach its
//     driver (job-wake and chat); Selection.Missing checks it against the
//     keep-set so a launcher can refuse before spawning a mute worker.
//   - A registry this build cannot vouch for fails closed with *SkewError.
//   - Diff renders how one Resolved differs from another as at most maxLines
//     short, printable lines in a fixed order, never printing the token, env
//     values or digests, or a URL that may carry a credential, for an
//     approver to read (FDR 0022 decision 4). Its full contract is on Diff.
//     It reads Canonical and does not change it, so it is outside the
//     ProtocolVersion rules below.
//   - Resolved.RedactedForm renders the definition with nothing secret in it
//     (the url as scheme://host[:port] only, or RedactedURL where Diff would
//     redact it; env as keys only; the token as token_set), and clown stores it beside each session pin.
//     RedactedFormByDigest reads it back by digest from clown's
//     session-names sidecar, for a caller that holds only an old digest
//     whose definition no longer resolves. DiffRedacted is Diff over two
//     such forms (FDR 0022 slice 3c, clown#298). The redacted form has its
//     own version (RedactedFormVersion), is derived from Canonical, and
//     never feeds the digest, so it is also outside the ProtocolVersion
//     rules. It is display only: it cannot be checked against the digest.
//
// # Protocol version
//
// ProtocolVersion versions the profiles.toml schema, the registry stamp and
// the canonical form below together. A launcher pins the constant it was built
// against; bumping it is a breaking change announced to consumers first.
// Protocol 2 added `base` and env-value digests. This build reads registries
// stamped 1 or 2; the digest depends only on the profile and this build's
// protocol, never on the registry's stamp.
//
// # Canonical form and digest
//
// Canonical is the profile rendered to JSON under these rules, so two builds
// of clown produce the same bytes for the same definition:
//
//   - one object, keys in this fixed order: protocol, name, display, provider,
//     backend, model, url, token_set, env, context_servers, context_excluded,
//     base;
//   - every key is always present: an unset string is "", never omitted;
//   - protocol is the integer ProtocolVersion; no floats occur anywhere;
//   - token is NEVER included: token_set is whether one is configured, so
//     rotating a token does not change the digest but adding or removing one
//     does;
//   - env and context_excluded are objects with keys sorted bytewise (unset
//     renders {}); env's values are the lowercase-hex sha256 of each value as
//     written, never the value; context_excluded's tool lists are sorted and
//     deduplicated;
//   - base is the profile's base exactly as written, "" when unset;
//   - context_servers is a sorted, deduplicated array, or null when the profile
//     has no saved selection (every server is kept) — never [] (a saved empty
//     selection is rejected by profile.Validate);
//   - strings are UTF-8, escaped per encoding/json except that <, > and & are
//     written literally; no insignificant whitespace, no trailing newline;
//   - values are verbatim from the registry: ${ENV} references are NOT
//     expanded. The token is withheld and env values are digests, but a
//     literal credential written into the URL IS in Canonical (clown#292), so
//     treat Canonical as sensitive (log and store the digest, not the bytes).
//     A low-entropy env value is guessable from its digest.
//
// DigestSHA256 is the lowercase hex sha256 of Canonical. The launcher renders
// it as a markl-id itself; this package depends on no markl-id library.
//
// Known limit: builtin profiles are compiled into whichever clown is linked, so
// a launcher pinning a builtin's digest detects drift only when it re-links a
// newer clown, not when the clown binary on PATH changes underneath it.
package profilelookup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"

	"code.linenisgreat.com/clown/internal/profile"
	"code.linenisgreat.com/clown/internal/userpath"
)

// ProtocolVersion is the registry/canonical-form protocol this build speaks.
const ProtocolVersion = profile.ProtocolVersion

// JobsPluginName is the name of the plugin clown synthesizes to carry the job
// platform (troupe chat, ringmaster); its servers are what a worker needs to
// reach its driver.
const JobsPluginName = "clown-builtin-jobs"

// RequiredServers returns the servers, named as in a profile's keep-set
// ("<plugin>/<server>"), that a worker needs in order to say hello and report
// back to its driver: job control and chat.
func RequiredServers() []string {
	return []string{JobsPluginName + "/ringmaster", JobsPluginName + "/troupe"}
}

// ErrNotFound is wrapped by Resolve's error when no builtin or user profile
// has the requested name.
var ErrNotFound = errors.New("profile not found")

// SkewError reports a registry this build cannot vouch for: written by a clown
// speaking another protocol, by one that predates version stamping, or holding
// fields this build does not know. Resolve returns it so a launcher can fail
// closed and print the message naming both sides.
type SkewError struct {
	RegistryPath     string
	RegistryVersion  string   // clown version that last wrote the registry; "" if unstamped
	RegistryProtocol int      // 0 if unstamped
	LibraryProtocol  int      // this build's ProtocolVersion
	UnknownFields    []string // keys present in the registry but unknown to this build
	// MisstampedBase names profiles carrying `base` under a stamp older than
	// protocol 2: a writer that did not know what base means.
	MisstampedBase []string
}

func (e *SkewError) Error() string {
	reads := fmt.Sprintf("this library speaks profile protocol %d and reads protocols %d to %d", e.LibraryProtocol, profile.OldestReadableProtocol, e.LibraryProtocol)
	switch {
	case e.RegistryProtocol == 0:
		return fmt.Sprintf("profile registry %s was written by a clown that predates version stamping (%s); save any profile with `clown profile` to restamp it", e.RegistryPath, reads)
	case !profile.ReadableProtocol(e.RegistryProtocol):
		return fmt.Sprintf("profile registry %s was last written by clown %s (profile protocol %d); %s", e.RegistryPath, e.RegistryVersion, e.RegistryProtocol, reads)
	case len(e.MisstampedBase) > 0:
		return fmt.Sprintf("profile registry %s is stamped profile protocol %d (by clown %s) but profiles %s carry base, which needs protocol 2; save it with a clown that speaks protocol 2, or remove base", e.RegistryPath, e.RegistryProtocol, e.RegistryVersion, strings.Join(e.MisstampedBase, ", "))
	default:
		return fmt.Sprintf("profile registry %s (last written by clown %s) holds fields unknown to this library (%s): %s", e.RegistryPath, e.RegistryVersion, reads, strings.Join(e.UnknownFields, ", "))
	}
}

// BaseError reports a profile whose `base` cannot be vouched for: it names no
// builtin, is malformed, or pins a digest the builtin no longer has. Resolve
// returns it so a launcher can refuse with the message.
type BaseError struct {
	Profile string
	Base    string // the base value as written
	Pinned  string // the pinned digest, "" when unpinned
	Current string // the builtin's current digest, "" when it was not resolved
	Reason  string
}

func (e *BaseError) Error() string {
	if e.Pinned != "" && e.Current != "" {
		return fmt.Sprintf("profile %q: base %q pins sha256 %s, but the builtin now has %s; re-pin it after checking the builtin's change", e.Profile, e.Base, e.Pinned, e.Current)
	}
	return fmt.Sprintf("profile %q: base %q: %s", e.Profile, e.Base, e.Reason)
}

// Source says which layer a profile came from.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
)

// Options tunes Resolve. The zero value reads the same registry file the
// clown binary does.
type Options struct {
	// RegistryPath overrides the user profiles.toml location (tests, or a
	// launcher that was told explicitly). Empty uses clown's discovery:
	// $XDG_CONFIG_HOME/clown/profiles.toml with the legacy juggler fallback.
	RegistryPath string
}

// Selection is a profile's saved --cheap-context keep-set.
type Selection struct {
	// Selective is false when the profile has no saved selection: every
	// discovered server is kept. A true Selective with an empty Kept cannot
	// occur (profile.Validate rejects it); it would mean "keeps nothing".
	Selective bool
	// Kept is the sorted set of servers ("<plugin>/<server>") the profile
	// keeps; nil when !Selective.
	Kept []string
	// Excluded maps a kept server to the sorted tool names the profile drops
	// from it; empty when none.
	Excluded map[string][]string
}

// Keeps reports whether the profile keeps server.
func (s Selection) Keeps(server string) bool {
	if !s.Selective {
		return true
	}
	return slices.Contains(s.Kept, server)
}

// Missing returns the members of required that the selection drops, in
// required's order. A non-selective profile misses nothing.
func (s Selection) Missing(required []string) []string {
	var out []string
	for _, r := range required {
		if !s.Keeps(r) {
			out = append(out, r)
		}
	}
	return out
}

// CrippledTools returns, for each of required that the selection keeps, the
// tools it nonetheless excludes — a kept server with its tools stripped is as
// mute as a dropped one. Empty when nothing is excluded.
func (s Selection) CrippledTools(required []string) map[string][]string {
	out := map[string][]string{}
	for _, r := range required {
		if tools := s.Excluded[r]; s.Keeps(r) && len(tools) > 0 {
			out[r] = tools
		}
	}
	return out
}

// RegistryStamp is what the user registry says about its last writer.
type RegistryStamp struct {
	Present         bool   // a user registry file exists
	ClownVersion    string // clown version that last wrote it
	ProtocolVersion int
}

// Resolved is a profile as the clown binary would launch it, minus secrets.
type Resolved struct {
	Name     string
	Display  string
	Provider string
	Backend  string
	Model    string
	Source   Source

	Selection Selection

	// Env maps each env key to the lowercase-hex sha256 of its value as
	// written (${VAR} references unexpanded), never the value itself. Equal
	// digests mean equal values; a low-entropy value is guessable from its
	// digest, so this is for comparison, not secrecy.
	Env map[string]string

	// Base is the builtin this profile declares it narrows, resolved exactly
	// as a top-level Resolve of that builtin would be (Source is always
	// SourceBuiltin, even when a user profile shadows the name); nil when
	// the profile has no base.
	Base *Resolved

	// Canonical is the canonical JSON of the definition (see package doc) and
	// DigestSHA256 the lowercase hex sha256 of it.
	Canonical    []byte
	DigestSHA256 string

	Registry RegistryStamp
}

// Resolve looks up name among the builtin profiles and the user registry (a
// user profile shadows a builtin of the same name, as at launch). It returns an
// error wrapping ErrNotFound for an unknown name, a *SkewError when the user
// registry is not vouched for, and the profile.Validate error for an invalid
// definition.
func Resolve(name string, opts Options) (*Resolved, error) {
	builtin, err := profile.Builtin()
	if err != nil {
		return nil, err
	}
	reg, stamp, err := loadUserRegistry(opts)
	if err != nil {
		return nil, err
	}

	// Same merge and same first-match lookup as the clown binary
	// (cmd/clown/main.go), so a registry that repeats a name resolves to the
	// entry `clown --profile NAME` would launch.
	merged := profile.Merge(builtin, reg.Profiles)
	found, ok := findProfile(merged, name)
	if !ok {
		return nil, fmt.Errorf("%w: %q (known: %s)", ErrNotFound, name, strings.Join(names(merged), ", "))
	}
	source := SourceBuiltin
	if _, inUser := findProfile(reg.Profiles, name); inUser {
		source = SourceUser
	}
	r, err := resolved(found, source, stamp)
	if err != nil {
		return nil, err
	}
	if found.Base != "" {
		if r.Base, err = resolveBase(found, builtin); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// resolved validates p and renders everything Resolve reports about it except
// its base.
func resolved(p profile.Profile, source Source, stamp RegistryStamp) (*Resolved, error) {
	if err := profile.Validate(p); err != nil {
		return nil, err
	}
	canonical, err := Canonicalize(p)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return &Resolved{
		Name:         p.Name,
		Display:      p.Display,
		Provider:     p.Provider,
		Backend:      p.Backend,
		Model:        p.Model,
		Source:       source,
		Selection:    selectionOf(p),
		Env:          envDigests(p.Env),
		Canonical:    canonical,
		DigestSHA256: hex.EncodeToString(sum[:]),
		Registry:     stamp,
	}, nil
}

// resolveBase resolves p's base among the builtins only (a user profile
// shadowing the name is not a base) and checks its pin.
func resolveBase(p profile.Profile, builtin []profile.Profile) (*Resolved, error) {
	name, pin, err := profile.ParseBase(p.Base)
	if err != nil {
		return nil, &BaseError{Profile: p.Name, Base: p.Base, Reason: err.Error()}
	}
	b, ok := findProfile(builtin, name)
	if !ok {
		return nil, &BaseError{Profile: p.Name, Base: p.Base, Reason: fmt.Sprintf("no builtin profile is named %q (builtins: %s)", name, strings.Join(names(builtin), ", "))}
	}
	r, err := resolved(b, SourceBuiltin, RegistryStamp{})
	if err != nil {
		return nil, err
	}
	if pin != "" && pin != r.DigestSHA256 {
		return nil, &BaseError{Profile: p.Name, Base: p.Base, Pinned: pin, Current: r.DigestSHA256}
	}
	return r, nil
}

// envDigests maps each env key to the lowercase-hex sha256 of its value.
func envDigests(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		sum := sha256.Sum256([]byte(v))
		out[k] = hex.EncodeToString(sum[:])
	}
	return out
}

// loadUserRegistry reads and vets the user registry. An absent file is an
// empty, unstamped, un-vetted registry (builtins only); a present one must pass
// the skew check.
func loadUserRegistry(opts Options) (profile.Registry, RegistryStamp, error) {
	path := opts.RegistryPath
	if path == "" {
		p, _, err := userpath.ConfigFile("profiles.toml")
		if err != nil {
			// No home: nowhere a user registry could live, same as the binary.
			return profile.Registry{}, RegistryStamp{}, nil
		}
		path = p
	}
	reg, err := profile.LoadRegistry(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return profile.Registry{}, RegistryStamp{}, nil
		}
		return profile.Registry{}, RegistryStamp{}, err
	}
	stamp := RegistryStamp{Present: true, ClownVersion: reg.Stamp.ClownVersion, ProtocolVersion: reg.Stamp.Protocol}
	misstamped := reg.MisstampedBase()
	if !profile.ReadableProtocol(reg.Stamp.Protocol) || len(reg.Undecoded) > 0 || len(misstamped) > 0 {
		return profile.Registry{}, stamp, &SkewError{
			RegistryPath:     path,
			RegistryVersion:  reg.Stamp.ClownVersion,
			RegistryProtocol: reg.Stamp.Protocol,
			LibraryProtocol:  ProtocolVersion,
			UnknownFields:    reg.Undecoded,
			MisstampedBase:   misstamped,
		}
	}
	return reg, stamp, nil
}

func findProfile(ps []profile.Profile, name string) (profile.Profile, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return profile.Profile{}, false
}

func names(ps []profile.Profile) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func selectionOf(p profile.Profile) Selection {
	if p.ContextServers == nil {
		return Selection{}
	}
	return Selection{
		Selective: true,
		Kept:      sortedUnique(p.ContextServers),
		Excluded:  excludedOf(p.ContextExcluded),
	}
}

func excludedOf(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for server, tools := range in {
		if u := sortedUnique(tools); len(u) > 0 {
			out[server] = u
		}
	}
	return out
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

// canonical is the wire shape of the canonical form; field order IS the key
// order. Do not reorder, add omitempty, or add fields without bumping
// ProtocolVersion.
type canonical struct {
	Protocol        int                 `json:"protocol"`
	Name            string              `json:"name"`
	Display         string              `json:"display"`
	Provider        string              `json:"provider"`
	Backend         string              `json:"backend"`
	Model           string              `json:"model"`
	URL             string              `json:"url"`
	TokenSet        bool                `json:"token_set"`
	Env             map[string]string   `json:"env"`
	ContextServers  []string            `json:"context_servers"`
	ContextExcluded map[string][]string `json:"context_excluded"`
	Base            string              `json:"base"`
}

// Canonicalize renders p in the canonical form documented on the package.
func Canonicalize(p profile.Profile) ([]byte, error) {
	c := canonical{
		Protocol:        ProtocolVersion,
		Name:            p.Name,
		Display:         p.Display,
		Provider:        p.Provider,
		Backend:         p.Backend,
		Model:           p.Model,
		URL:             p.URL,
		TokenSet:        p.Token != "",
		Env:             envDigests(p.Env),
		ContextExcluded: excludedOf(p.ContextExcluded),
		Base:            p.Base,
	}
	if p.ContextServers != nil {
		c.ContextServers = sortedUnique(p.ContextServers)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("canonicalize profile %q: %w", p.Name, err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

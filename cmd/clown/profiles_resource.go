package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"code.linenisgreat.com/clown/internal/0/profile"
	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// The profile resources: the clown-builtin-diagnostics server
// (`clown disconnects-serve`) also serves the named-profile registry
// read-only, so an agent session can see what a profile is without a
// terminal or a read of ~/.config:
//
//   - clown://profiles: every merged profile (builtin and user, a user
//     profile shadowing a builtin of its name) as name, display, provider,
//     backend, source and digest. Served to every session.
//   - clown://profiles/<name>: one definition, redacted. Served only when
//     the launch passed `--profiles definitions`, which clown does for an
//     unconfined launch and never for a confined one (--agent-scope): a
//     definition maps how the fleet's profiles are wired, which a confined
//     worker does not get by default.
//
// Both are read from the registry on every request, by the same calls
// `clown profile list` and `clown profile digest` make, so the digest is
// the one `clown profile digest` prints from the same binary. Nothing
// secret is served: a definition is profilelookup's RedactedForm (url as
// scheme://host[:port] or redacted, token as token_set, env as keys only),
// never the canonical form, which can hold a literal credential in the url.
// Profiles carry no prompts or permission posture, so neither is served.

// profilesURI is the list resource.
const profilesURI = "clown://profiles"

// Values of disconnects-serve's --profiles.
const (
	profilesServeList        = "list"
	profilesServeDefinitions = "definitions"
)

// profilesView is what the server serves of the registry: Definitions turns
// on clown://profiles/<name>, RegistryPath overrides the registry (tests).
type profilesView struct {
	Definitions  bool
	RegistryPath string
}

// profileListEntry is one row of clown://profiles. Digest is empty and
// Error set when this build cannot vouch for the profile (what `clown
// profile digest` would refuse); URI is set only when definitions are served.
type profileListEntry struct {
	Name     string `json:"name"`
	Display  string `json:"display"`
	Provider string `json:"provider"`
	Backend  string `json:"backend"`
	Source   string `json:"source"`
	Digest   string `json:"digest,omitempty"`
	Error    string `json:"error,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// profileList is clown://profiles. Error is a refusal of the user registry
// as a whole (a skewed or unreadable one): the builtins are still listed.
type profileList struct {
	Profiles []profileListEntry `json:"profiles"`
	Error    string             `json:"error,omitempty"`
}

// profileMoxyView is a definition's moxy table.
type profileMoxyView struct {
	Placement  string   `json:"placement"`
	AllowTools []string `json:"allow_tools"`
	Tools      []string `json:"tools"`
}

// profileDefinition is clown://profiles/<name>: the redacted form's fields
// (decoded from RedactedForm, so its redaction is the one rule) plus what
// carries no secret.
type profileDefinition struct {
	Name             string              `json:"name"`
	Display          string              `json:"display"`
	Provider         string              `json:"provider"`
	Backend          string              `json:"backend"`
	Model            string              `json:"model"`
	URL              string              `json:"url"`
	TokenSet         bool                `json:"token_set"`
	EnvKeys          []string            `json:"env_keys"`
	ContextServers   []string            `json:"context_servers"`
	ContextExcluded  map[string][]string `json:"context_excluded"`
	Base             string              `json:"base"`
	Protocol         int                 `json:"protocol"`
	Moxy             *profileMoxyView    `json:"moxy"`
	Source           string              `json:"source"`
	OverridesBuiltin bool                `json:"overrides_builtin"`
	Digest           string              `json:"digest"`
	BaseDigest       string              `json:"base_digest,omitempty"`
	Registry         profileRegistryView `json:"registry"`
}

type profileRegistryView struct {
	Present      bool   `json:"present"`
	ClownVersion string `json:"clown_version,omitempty"`
	Protocol     int    `json:"protocol,omitempty"`
}

// servesProfileDefinitions reports whether the launch's diagnostics server
// serves profile definitions: an unconfined launch does, a confined one
// (--agent-scope) gets the list only.
func servesProfileDefinitions(flags parsedFlags) bool { return flags.agentScopeSpec == "" }

// profileResources is what resources/list adds: the list resource only (the
// definitions are named by it, and by the template, when served).
func profileResources(v profilesView) []map[string]any {
	desc := "every profile's name, display, provider/backend, source and digest"
	if v.Definitions {
		desc += "; each profile's redacted definition is " + profilesURI + "/{name}"
	}
	return []map[string]any{{"uri": profilesURI, "name": "clown profiles", "description": desc, "mimeType": disconnectsMIME}}
}

// profilesInstructionsTail ends the server's instructions sentence on the
// profiles.
func profilesInstructionsTail(v profilesView) string {
	if v.Definitions {
		return "; " + profilesURI + "/{name} is one profile's redacted definition"
	}
	return "; definitions are not served to this session"
}

// profileURI is the definition resource of name.
func profileURI(name string) string { return profilesURI + "/" + url.PathEscape(name) }

// profileNameOf is the profile a definition URI names, or false for any
// other URI.
func profileNameOf(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, profilesURI+"/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	name, err := url.PathUnescape(rest)
	return name, err == nil && name != ""
}

// isProfilesURI reports whether uri is one of the profile resources, served
// or not.
func isProfilesURI(uri string) bool {
	return uri == profilesURI || strings.HasPrefix(uri, profilesURI+"/")
}

// list renders clown://profiles.
func (v profilesView) list() profileList {
	builtin, user, path, err := loadProfileSets(v.RegistryPath)
	out := profileList{Profiles: []profileListEntry{}}
	var skew *registrySkewError
	if err != nil && !errors.As(err, &skew) {
		out.Error = err.Error()
		return out
	}
	if skew != nil {
		out.Error = skew.Error()
	}
	isUser := map[string]bool{}
	for _, p := range user {
		isUser[p.Name] = true
	}
	isBuiltin := map[string]bool{}
	for _, p := range builtin {
		isBuiltin[p.Name] = true
	}
	for _, p := range profile.Merge(builtin, user) {
		e := profileListEntry{Name: p.Name, Display: p.Display, Provider: p.Provider, Backend: p.Backend, Source: profileSourceLabel(isBuiltin[p.Name], isUser[p.Name])}
		if r, rerr := profilelookup.Resolve(p.Name, profilelookup.Options{RegistryPath: path}); rerr != nil {
			e.Error = rerr.Error()
		} else {
			e.Digest = r.DigestSHA256
		}
		if v.Definitions {
			e.URI = profileURI(p.Name)
		}
		out.Profiles = append(out.Profiles, e)
	}
	return out
}

// profileSourceLabel is `clown profile list`'s SOURCE column.
func profileSourceLabel(builtin, user bool) string {
	switch {
	case user && builtin:
		return "user override"
	case user:
		return "user"
	}
	return "builtin"
}

// definition renders clown://profiles/<name>; false for an unknown name.
// A known profile this build cannot vouch for reads as {name, error}.
func (v profilesView) definition(name string) (any, bool) {
	builtin, _, path, err := loadProfileSets(v.RegistryPath)
	var skew *registrySkewError
	if err != nil && !errors.As(err, &skew) {
		return map[string]string{"name": name, "error": err.Error()}, true
	}
	r, err := profilelookup.Resolve(name, profilelookup.Options{RegistryPath: path})
	if errors.Is(err, profilelookup.ErrNotFound) {
		return nil, false
	}
	if err != nil {
		return map[string]string{"name": name, "error": err.Error()}, true
	}
	form, err := r.RedactedForm()
	if err != nil {
		return map[string]string{"name": name, "error": err.Error()}, true
	}
	var d profileDefinition
	if err := json.Unmarshal(form, &d); err != nil {
		return map[string]string{"name": name, "error": err.Error()}, true
	}
	if d.EnvKeys == nil {
		d.EnvKeys = []string{}
	}
	if r.Moxy != nil {
		d.Moxy = &profileMoxyView{Placement: r.Moxy.Placement, AllowTools: r.Moxy.AllowTools, Tools: r.Moxy.Tools}
	}
	d.Source = string(r.Source)
	d.OverridesBuiltin = r.Source == profilelookup.SourceUser && slices.ContainsFunc(builtin, func(p profile.Profile) bool { return p.Name == name })
	d.Digest = r.DigestSHA256
	if r.Base != nil {
		d.BaseDigest = r.Base.DigestSHA256
	}
	d.Registry = profileRegistryView{Present: r.Registry.Present, ClownVersion: r.Registry.ClownVersion, Protocol: r.Registry.ProtocolVersion}
	return d, true
}

// read answers resources/read of a profile URI: the JSON
// text, or false (resource not found) for an unknown profile, a malformed
// URI, or a definition when definitions are not served.
func (v profilesView) read(uri string) (string, bool) {
	var doc any
	if uri == profilesURI {
		doc = v.list()
	} else {
		name, ok := profileNameOf(uri)
		if !ok || !v.Definitions {
			return "", false
		}
		if doc, ok = v.definition(name); !ok {
			return "", false
		}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", false
	}
	return string(b), true
}

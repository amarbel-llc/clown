package profile

import (
	"fmt"
	"sort"

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
	// discovered/fetched catalog is silently skipped when applying the
	// selection — the live catalog is the source of truth, this saved data
	// is best-effort intent, not a hard requirement (matches
	// applyCheapContextSelection's existing best-effort posture elsewhere,
	// e.g. a failed exclude-tools push is logged and skipped, not fatal).
	ContextServers  []string            `toml:"context_servers,omitempty"`
	ContextExcluded map[string][]string `toml:"context_excluded,omitempty"`
}

type file struct {
	Profile []Profile `toml:"profile"`
}

func Load(path string) ([]Profile, error) {
	var f file
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("load profiles %s: %w", path, err)
	}
	return f.Profile, nil
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
	"claude":   {"anthropic": true, "gateway": true, "local": true},
	"opencode": {"anthropic": true, "gateway": true, "local": true},
	"crush":    {"anthropic": true, "gateway": true, "local": true},
}

func Validate(p Profile) error {
	backends, ok := validCombos[p.Provider]
	if !ok {
		return fmt.Errorf("profile %q: unknown provider %q (valid: claude, opencode, crush)", p.Name, p.Provider)
	}
	if !backends[p.Backend] {
		return fmt.Errorf("profile %q: provider %q does not support backend %q", p.Name, p.Provider, p.Backend)
	}
	if p.Backend == "gateway" {
		if p.URL == "" {
			return fmt.Errorf("profile %q: backend gateway requires url", p.Name)
		}
		if p.Token == "" {
			return fmt.Errorf("profile %q: backend gateway requires token", p.Name)
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

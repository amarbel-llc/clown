package profile

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

type Profile struct {
	Name     string `toml:"name"`
	Display  string `toml:"display"`
	Provider string `toml:"provider"`
	Backend  string `toml:"backend"`
	Model    string `toml:"model"`
	URL      string `toml:"url"`
	Token    string `toml:"token"`
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
	"claude":   {"anthropic": true, "local": true},
	"opencode": {"anthropic": true, "gateway": true, "local": true},
	"clownbox": {"anthropic": true, "local": true},
}

func Validate(p Profile) error {
	backends, ok := validCombos[p.Provider]
	if !ok {
		return fmt.Errorf("profile %q: unknown provider %q (valid: claude, opencode, clownbox)", p.Name, p.Provider)
	}
	if !backends[p.Backend] {
		return fmt.Errorf("profile %q: provider %q does not support backend %q", p.Name, p.Provider, p.Backend)
	}
	if p.Provider == "opencode" && p.Backend == "gateway" {
		if p.URL == "" {
			return fmt.Errorf("profile %q: backend gateway requires url", p.Name)
		}
		if p.Token == "" {
			return fmt.Errorf("profile %q: backend gateway requires token", p.Name)
		}
	}
	return nil
}

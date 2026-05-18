package circusmodels

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed registry.json
var registryJSON []byte

// RegistryEntry describes a downloadable model in the baked-in
// registry. The JSON tags match the schema in registry.json.
type RegistryEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Description string `json:"description"`
}

// Registry returns the baked-in list of downloadable models. The
// returned slice is freshly allocated per call; callers may mutate it.
func Registry() ([]RegistryEntry, error) {
	var entries []RegistryEntry
	if err := json.Unmarshal(registryJSON, &entries); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return entries, nil
}

// FindEntry returns the entry with the given name from entries.
// The boolean is false when no such entry exists.
func FindEntry(name string, entries []RegistryEntry) (RegistryEntry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return RegistryEntry{}, false
}

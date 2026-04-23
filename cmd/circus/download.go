package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed registry.json
var registryJSON []byte

type registryEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Description string `json:"description"`
}

func loadRegistry() ([]registryEntry, error) {
	var entries []registryEntry
	if err := json.Unmarshal(registryJSON, &entries); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return entries, nil
}

func findInRegistry(name string, entries []registryEntry) (registryEntry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return registryEntry{}, false
}

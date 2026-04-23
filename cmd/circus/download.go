package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, expected)
	}
	return nil
}

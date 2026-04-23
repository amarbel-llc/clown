package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistry_ParsesAllFields(t *testing.T) {
	entries, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one registry entry")
	}
	for _, e := range entries {
		if e.Name == "" {
			t.Errorf("entry has empty name: %+v", e)
		}
		if e.URL == "" {
			t.Errorf("entry %q has empty url", e.Name)
		}
		if len(e.SHA256) != 64 {
			t.Errorf("entry %q sha256 must be 64 hex chars, got %d", e.Name, len(e.SHA256))
		}
		if e.Size == 0 {
			t.Logf("warning: entry %q has size=0 (placeholder)", e.Name)
		}
		if e.Description == "" {
			t.Errorf("entry %q has empty description", e.Name)
		}
	}
}

func TestLoadRegistry_ContainsExpectedModels(t *testing.T) {
	entries, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 registry entries, got %d", len(entries))
	}
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"qwen3-0.6b", "qwen3-1.7b", "qwen3-4b", "gemma3-1b", "gemma3-4b"} {
		if !names[want] {
			t.Errorf("expected model %q in registry", want)
		}
	}
}

func TestVerifySHA256_Match(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	content := []byte("hello circus")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(content)
	digest := hex.EncodeToString(h[:])
	if err := verifySHA256(path, digest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifySHA256_Mismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	if err := os.WriteFile(path, []byte("hello circus"), 0644); err != nil {
		t.Fatal(err)
	}
	err := verifySHA256(path, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error for mismatch")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("expected mismatch error, got: %v", err)
	}
}

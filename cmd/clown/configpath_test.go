package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserConfigPathCanonical(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("HOME", dir)
	canonical := filepath.Join(dir, "xdg", "clown", "profiles.toml")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, legacy, err := userConfigPath("profiles.toml")
	if err != nil || got != canonical || legacy {
		t.Fatalf("got %q legacy=%v err=%v, want canonical", got, legacy, err)
	}
}

func TestUserConfigPathLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	legacyPath := filepath.Join(dir, ".config", "juggler", "profiles.toml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, legacy, err := userConfigPath("profiles.toml")
	if err != nil || got != legacyPath || !legacy {
		t.Fatalf("got %q legacy=%v err=%v, want legacy fallback", got, legacy, err)
	}
}

func TestUserConfigPathNeitherExists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	got, legacy, err := userConfigPath("profiles.toml")
	want := filepath.Join(dir, ".config", "clown", "profiles.toml")
	if err != nil || got != want || legacy {
		t.Fatalf("got %q legacy=%v err=%v, want canonical-nonexistent", got, legacy, err)
	}
}

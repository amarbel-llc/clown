package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amarbel-llc/clown/internal/profile"
)

func TestReadOpenRouterConfigMissingFileIsZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := readOpenRouterConfig()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg != (openRouterConfig{}) {
		t.Errorf("expected zero config, got %+v", cfg)
	}
}

func TestReadOpenRouterConfigParse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "circus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "# openrouter\nurl = \"https://openrouter.ai/api/v1\"\ntoken = \"sk-or-xyz\"\nmodel = \"openai/gpt-4o-mini\"\n"
	if err := os.WriteFile(filepath.Join(dir, "openrouter.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readOpenRouterConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://openrouter.ai/api/v1" || cfg.Token != "sk-or-xyz" || cfg.Model != "openai/gpt-4o-mini" {
		t.Errorf("unexpected parse: %+v", cfg)
	}
}

func TestResolveOpenRouterProfileWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "circus")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "openrouter.toml"), []byte("token=\"from-file\"\n"), 0o600)

	prof := &profile.Profile{URL: "https://gw/v1", Token: "from-profile", Model: "m"}
	cfg, err := resolveOpenRouter(prof)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "from-profile" {
		t.Errorf("profile token should win, got %q", cfg.Token)
	}
}

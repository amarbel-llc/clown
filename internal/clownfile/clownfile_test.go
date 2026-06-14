package clownfile

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverCascadeDeeperOverrides(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	child := filepath.Join(repo, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	// Shallow (home): provider=claude, model=opus, env A=1.
	write(t, home, "[profile]\nprovider = \"claude\"\nmodel = \"opus\"\n[profile.env]\nA = \"1\"\n")
	// Deep (repo): override provider, add env B; model + A inherited.
	write(t, repo, "[profile]\nprovider = \"codex\"\n[profile.env]\nB = \"2\"\n")

	cf, err := Discover(child, home)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "codex" {
		t.Errorf("provider = %q, want codex (deeper overrides)", cf.Profile.Provider)
	}
	if cf.Profile.Model != "opus" {
		t.Errorf("model = %q, want opus (inherited from shallower)", cf.Profile.Model)
	}
	if cf.Profile.Env["A"] != "1" || cf.Profile.Env["B"] != "2" {
		t.Errorf("env = %v, want A=1,B=2 (union)", cf.Profile.Env)
	}
}

func TestDiscoverAbsentIsZero(t *testing.T) {
	home := t.TempDir()
	cf, err := Discover(home, home)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "" || cf.Profile.Model != "" || len(cf.Profile.Env) != 0 {
		t.Errorf("absent clownfile must yield zero value, got %+v", cf)
	}
}

func TestDiscoverMalformedErrors(t *testing.T) {
	home := t.TempDir()
	write(t, home, "this is not = valid toml [[[")
	if _, err := Discover(home, home); err == nil {
		t.Fatal("malformed clownfile must error")
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// A confined unit's nix must not fetch the global flake registry (its egress
// allowlist does not carry it, so every `nix develop` stalled retrying):
// stageAgentHome seeds the agent-home's user nix.conf with exactly these lines.
func TestStageAgentHomeSeedsTheNixConf(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	agentHome := t.TempDir()
	if _, err := stageAgentHome(agentscope.Spec{OperatorHome: t.TempDir()}, agentHome, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agentHome, ".config", "nix", "nix.conf")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "flake-registry =\ndownload-attempts = 1\nconnect-timeout = 5\n"; string(got) != want {
		t.Fatalf("nix.conf = %q, want %q", got, want)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("nix.conf mode %v, %v; want 0600", fi.Mode(), err)
	}
}

// An agent-home that already has a nix.conf keeps it (a restart relaunches
// into the same home; whatever the session or operator put there stays).
func TestStageAgentHomeKeepsAnExistingNixConf(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	agentHome := t.TempDir()
	dir := filepath.Join(agentHome, ".config", "nix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nix.conf")
	if err := os.WriteFile(path, []byte("keep-going = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stageAgentHome(agentscope.Spec{OperatorHome: t.TempDir()}, agentHome, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "keep-going = true\n" {
		t.Fatalf("nix.conf = %q, want it untouched", got)
	}
}

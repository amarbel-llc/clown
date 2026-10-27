package main

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// The agent can write its own agent-home, and the frontend stages it as the
// operator on every launch: a symlink planted at .config or .config/nix must
// not make clown create files where it points.
func TestSeedNixConfRefusesASymlinkedParent(t *testing.T) {
	for _, plant := range []string{".config", filepath.Join(".config", "nix")} {
		agentHome, outside := t.TempDir(), t.TempDir()
		link := filepath.Join(agentHome, plant)
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if err := seedNixConf(agentHome); err == nil {
			t.Errorf("symlink at %s: want an error", plant)
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Errorf("symlink at %s: clown wrote %d entries through it", plant, len(entries))
		}
	}
}

// A seed that cannot be written does not fail the launch: the cost of no seed
// is a slow `nix develop`, not a missing session.
func TestStageAgentHomeSurvivesAnUnseedableNixConf(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	agentHome := t.TempDir()
	// .config is a regular file, so the nix dir cannot be made under it.
	if err := os.WriteFile(filepath.Join(agentHome, ".config"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stageAgentHome(agentscope.Spec{OperatorHome: t.TempDir()}, agentHome, t.TempDir()); err != nil {
		t.Fatalf("stageAgentHome failed on an unseedable nix.conf: %v", err)
	}
}

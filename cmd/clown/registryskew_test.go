package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistry(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const knownProfile = `
[[profile]]
name = "mine"
provider = "claude"
backend = "anthropic"
`

// TestLoadProfileSetsRefusesARegistryItCannotReadInFull: an older clown must
// not silently drop restrictions a newer one wrote (audit 2026-10-22).
func TestLoadProfileSetsRefusesARegistryItCannotReadInFull(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"newer protocol": {
			knownProfile + "\n[registry]\nclown_version = \"9.9.9\"\nprotocol = \"2\"\n",
			"clown 9.9.9 (profile protocol 2)",
		},
		"unknown profile field": {
			knownProfile + "context_kept = [\"moxy/moxy\"]\n\n[registry]\nclown_version = \"0.4.4\"\nprotocol = \"1\"\n",
			"context_kept",
		},
		"malformed protocol": {
			knownProfile + "\n[registry]\nprotocol = \"two\"\n",
			"newer profile protocol",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := loadProfileSets(writeRegistry(t, c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "refuses it") {
				t.Errorf("err = %v, want a refusal naming %q", err, c.want)
			}
		})
	}
}

func TestLoadProfileSetsReadsCurrentAndUnstampedRegistries(t *testing.T) {
	for name, body := range map[string]string{
		"current":   knownProfile + "\n[registry]\nclown_version = \"0.4.4\"\nprotocol = \"1\"\n",
		"unstamped": knownProfile,
	} {
		t.Run(name, func(t *testing.T) {
			_, user, _, err := loadProfileSets(writeRegistry(t, body))
			if err != nil || len(user) != 1 || user[0].Name != "mine" {
				t.Errorf("user = %+v, err = %v; want the one profile", user, err)
			}
		})
	}
}

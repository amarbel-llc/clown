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
			knownProfile + "\n[registry]\nclown_version = \"9.9.9\"\nprotocol = \"3\"\n",
			"profile protocol 3, written by clown 9.9.9",
		},
		"base under a protocol-1 stamp": {
			knownProfile + "base = \"canary\"\n\n[registry]\nclown_version = \"0.4.4\"\nprotocol = \"1\"\n",
			"base on profiles mine under a protocol 1 stamp",
		},
		"base with no stamp": {
			knownProfile + "base = \"canary\"\n",
			"base on profiles mine under no protocol stamp",
		},
		"unknown profile field": {
			knownProfile + "context_kept = [\"moxy/moxy\"]\n\n[registry]\nclown_version = \"0.4.4\"\nprotocol = \"1\"\n",
			"context_kept",
		},
		"malformed protocol": {
			knownProfile + "\n[registry]\nprotocol = \"two\"\n",
			"unreadable protocol stamp",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := loadProfileSets(writeRegistry(t, c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "refuses the registry") ||
				strings.Contains(err.Error(), "-1") {
				t.Errorf("err = %v, want a refusal naming %q", err, c.want)
			}
		})
	}
}

const skewedRegistry = knownProfile + "context_kept = [\"moxy/moxy\"]\n\n[registry]\nclown_version = \"9.9.9\"\nprotocol = \"3\"\n"

// TestProfileListShowsBuiltinsBesideARefusedRegistry: list is read-only, so
// it shows what it can vouch for (the builtins), prints the refusal, exits
// nonzero, and lists nothing from the refused file.
func TestProfileListShowsBuiltinsBesideARefusedRegistry(t *testing.T) {
	var out, errOut strings.Builder
	code := listProfiles(writeRegistry(t, skewedRegistry), &out, &errOut)
	if code == 0 {
		t.Error("list exited 0 on a refused registry")
	}
	if !strings.Contains(out.String(), "canary") || strings.Contains(out.String(), "mine") {
		t.Errorf("list printed %q; want the builtins and nothing from the refused registry", out.String())
	}
	for _, want := range []string{"profile protocol 3, written by clown 9.9.9", "context_kept", "refuses the registry"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr %q does not say %q", errOut.String(), want)
		}
	}
}

// TestProfileVerbsThatChangeOrLaunchRefuseARefusedRegistry: remove and edit
// refuse before touching the file, as the launch path does (main.go returns
// on any loadProfileSets error).
func TestProfileVerbsThatChangeOrLaunchRefuseARefusedRegistry(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := filepath.Join(cfg, "clown", "profiles.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(skewedRegistry), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runProfileRemove("mine"); code == 0 {
		t.Error("profile remove ran on a refused registry")
	}
	if _, err := profileEditInteractive("mine"); err == nil || !strings.Contains(err.Error(), "refuses the registry") {
		t.Errorf("profile edit: err = %v, want the refusal", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != skewedRegistry {
		t.Errorf("the refused registry was rewritten:\n%s", raw)
	}
}

func TestLoadProfileSetsReadsCurrentAndUnstampedRegistries(t *testing.T) {
	for name, body := range map[string]string{
		"protocol 1":         knownProfile + "\n[registry]\nclown_version = \"0.4.4\"\nprotocol = \"1\"\n",
		"protocol 2 no base": knownProfile + "\n[registry]\nclown_version = \"0.4.5\"\nprotocol = \"2\"\n",
		"protocol 2 base":    knownProfile + "base = \"canary\"\n\n[registry]\nclown_version = \"0.4.5\"\nprotocol = \"2\"\n",
		"unstamped":          knownProfile,
	} {
		t.Run(name, func(t *testing.T) {
			_, user, _, err := loadProfileSets(writeRegistry(t, body))
			if err != nil || len(user) != 1 || user[0].Name != "mine" {
				t.Errorf("user = %+v, err = %v; want the one profile", user, err)
			}
		})
	}
}

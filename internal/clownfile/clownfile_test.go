package clownfile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeFile writes raw content to an arbitrary path (the burned-in base
// clownfile is a single file, not a `Filename` inside a discovered ancestor).
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

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

	cf, err := Discover(child, home, "")
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
	cf, err := Discover(home, home, "")
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
	if _, err := Discover(home, home, ""); err == nil {
		t.Fatal("malformed clownfile must error")
	}
}

// The burned-in base clownfile (RFC-0013 §1.3) is the lowest-precedence layer:
// an ancestor clownfile overrides it per key, but its un-overridden keys survive.
func TestDiscoverBaseIsLowestPrecedence(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(t.TempDir(), "default-clownfile")
	writeFile(t, base, "[profile]\nprovider = \"claude\"\n[attach]\nmultiplexer = \"zmx\"\n"+
		"start = [\"zmx\", \"attach\", \"{id}\", \"{entry}\"]\nresume-title = \"{id}\"\n")
	// User clownfile at home overrides multiplexer + provider; start/resume-title inherit from base.
	write(t, home, "[profile]\nprovider = \"codex\"\n[attach]\nmultiplexer = \"none\"\n")

	cf, err := Discover(home, home, base)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Profile.Provider != "codex" {
		t.Errorf("provider = %q, want codex (user overrides base)", cf.Profile.Provider)
	}
	if cf.Attach.Multiplexer != "none" {
		t.Errorf("multiplexer = %q, want none (user overrides base)", cf.Attach.Multiplexer)
	}
	if want := []string{"zmx", "attach", "{id}", "{entry}"}; !reflect.DeepEqual(cf.Attach.Start, want) {
		t.Errorf("start = %v, want %v (inherited from base)", cf.Attach.Start, want)
	}
	if cf.Attach.ResumeTitle != "{id}" {
		t.Errorf("resume-title = %q, want {id} (inherited from base)", cf.Attach.ResumeTitle)
	}
}

// With no ancestor clownfile, the burned-in base supplies the defaults.
func TestDiscoverBaseOnlyWhenNoAncestor(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(t.TempDir(), "default-clownfile")
	writeFile(t, base, "[attach]\nmultiplexer = \"zmx\"\nspawn-entry = [\"clown\", \"--\", \"{prompt}\"]\n")
	cf, err := Discover(home, home, base)
	if err != nil {
		t.Fatal(err)
	}
	if !cf.Attach.Enabled() {
		t.Fatal("base default must enable the multiplexer with no ancestor clownfile")
	}
	if want := []string{"clown", "--", "{prompt}"}; !reflect.DeepEqual(cf.Attach.SpawnEntry, want) {
		t.Errorf("spawn-entry = %v, want base default %v", cf.Attach.SpawnEntry, want)
	}
}

// An absent base path is non-fatal (dev builds leave it ""/missing); a present
// but malformed base is an error.
func TestDiscoverBaseAbsentAndMalformed(t *testing.T) {
	home := t.TempDir()
	cf, err := Discover(home, home, filepath.Join(home, "nonexistent-base"))
	if err != nil {
		t.Fatalf("absent base must be non-fatal, got %v", err)
	}
	if cf.Attach.Enabled() || cf.Profile.Provider != "" {
		t.Errorf("absent base must yield zero value, got %+v", cf)
	}
	bad := filepath.Join(t.TempDir(), "bad-base")
	writeFile(t, bad, "not = valid [[[")
	if _, err := Discover(home, home, bad); err == nil {
		t.Fatal("malformed base clownfile must error")
	}
}

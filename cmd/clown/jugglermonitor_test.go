package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

func TestJugglerPluginDirSynthesized(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JUGGLER_MCP", "")
	orig := buildcfg.JugglerCliPath
	buildcfg.JugglerCliPath = "/nix/store/x/bin/juggler"
	t.Cleanup(func() { buildcfg.JugglerCliPath = orig })

	dir, err := synthJugglerPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected a synthesized plugin dir when JugglerCliPath is set")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading synthesized manifest: %v", err)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, b)
	}
	if m.Name != "clown-builtin-juggler" {
		t.Fatalf("plugin name = %q, want clown-builtin-juggler", m.Name)
	}

	cb, err := os.ReadFile(filepath.Join(dir, "clown.json"))
	if err != nil {
		t.Fatalf("expected clown.json: %v", err)
	}
	var cfg struct {
		Version      int `json:"version"`
		StdioServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"stdioServers"`
	}
	if err := json.Unmarshal(cb, &cfg); err != nil {
		t.Fatalf("clown.json invalid: %v\n%s", err, cb)
	}
	srv, ok := cfg.StdioServers["juggler"]
	if !ok {
		t.Fatalf("clown.json missing stdioServers.juggler; got %s", cb)
	}
	if srv.Command != buildcfg.JugglerCliPath {
		t.Fatalf("command = %q, want the baked JugglerCliPath %q", srv.Command, buildcfg.JugglerCliPath)
	}
	if len(srv.Args) != 1 || srv.Args[0] != "mcp" {
		t.Fatalf("args = %v, want [mcp]", srv.Args)
	}
}

func TestJugglerPluginDirNoPathReturnsEmpty(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JUGGLER_MCP", "")
	orig := buildcfg.JugglerCliPath
	buildcfg.JugglerCliPath = ""
	t.Cleanup(func() { buildcfg.JugglerCliPath = orig })

	dir, err := synthJugglerPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("expected no plugin dir when JugglerCliPath is empty (dev build), got %q", dir)
	}
}

func TestJugglerPluginDirDisabledReturnsEmpty(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JUGGLER_MCP", "1")
	orig := buildcfg.JugglerCliPath
	buildcfg.JugglerCliPath = "/nix/store/x/bin/juggler"
	t.Cleanup(func() { buildcfg.JugglerCliPath = orig })

	dir, err := synthJugglerPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("expected no plugin dir when CLOWN_DISABLE_JUGGLER_MCP=1, got %q", dir)
	}
}

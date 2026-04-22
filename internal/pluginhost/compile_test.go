package pluginhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCompilePluginManifest_StripsMcpServers(t *testing.T) {
	input := []byte(`{
  "name": "moxy",
  "mcpServers": {
    "moxy": {"type": "stdio", "command": "/usr/bin/moxy"}
  }
}`)

	out, had, err := CompilePluginManifest(input)
	if err != nil {
		t.Fatalf("CompilePluginManifest: %v", err)
	}
	if !had {
		t.Fatalf("had = false, want true (mcpServers was present)")
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if _, present := got["mcpServers"]; present {
		t.Errorf("mcpServers still present after compile: %s", out)
	}
	if got["name"] != "moxy" {
		t.Errorf("name field lost or changed: %v", got["name"])
	}
}

func TestCompilePluginManifest_PreservesUnknownFields(t *testing.T) {
	input := []byte(`{
  "name": "alpha",
  "version": "0.1.0",
  "mcpServers": {"foo": {}},
  "hooks": {"SessionStart": [{"command": "echo hi"}]},
  "someFutureKey": {"nested": [1, 2, 3]}
}`)

	out, _, err := CompilePluginManifest(input)
	if err != nil {
		t.Fatalf("CompilePluginManifest: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"name", "version", "hooks", "someFutureKey"} {
		if _, present := got[key]; !present {
			t.Errorf("key %q was stripped but should have been preserved", key)
		}
	}
	if _, present := got["mcpServers"]; present {
		t.Errorf("mcpServers still present: %s", out)
	}
}

func TestCompilePluginManifest_NoOpWhenAbsent(t *testing.T) {
	input := []byte(`{"name": "bare", "skills": []}`)

	out, had, err := CompilePluginManifest(input)
	if err != nil {
		t.Fatalf("CompilePluginManifest: %v", err)
	}
	if had {
		t.Errorf("had = true, want false (mcpServers was absent)")
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got["name"] != "bare" {
		t.Errorf("name field lost or changed: %v", got["name"])
	}
}

func TestCompilePluginManifest_InvalidJSON(t *testing.T) {
	_, _, err := CompilePluginManifest([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestCompilePluginDir_SymlinksAndRewrites(t *testing.T) {
	source := t.TempDir()

	// Layout: clown.json + skills/ + hooks/ + .claude-plugin/plugin.json + .claude-plugin/marketplace.json
	mustWrite(t, filepath.Join(source, "clown.json"), `{"version":1,"httpServers":{}}`)
	mustMkdir(t, filepath.Join(source, "skills"))
	mustWrite(t, filepath.Join(source, "skills", "alpha.md"), "# alpha")
	mustMkdir(t, filepath.Join(source, "hooks"))
	mustWrite(t, filepath.Join(source, "hooks", "hook.sh"), "#!/bin/sh\n")

	mustMkdir(t, filepath.Join(source, ".claude-plugin"))
	mustWrite(t, filepath.Join(source, ".claude-plugin", "plugin.json"),
		`{"name":"demo","mcpServers":{"srv":{"command":"x"}},"skills":["skills/"]}`)
	mustWrite(t, filepath.Join(source, ".claude-plugin", "marketplace.json"),
		`{"displayName":"demo"}`)

	staged, err := CompilePluginDir(source)
	if err != nil {
		t.Fatalf("CompilePluginDir: %v", err)
	}
	defer os.RemoveAll(staged)

	// Top-level entries (non-.claude-plugin) must be symlinks.
	for _, name := range []string{"clown.json", "skills", "hooks"} {
		info, err := os.Lstat(filepath.Join(staged, name))
		if err != nil {
			t.Fatalf("missing top-level %s: %v", name, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("staged %s is not a symlink (mode %v)", name, info.Mode())
		}
	}

	// .claude-plugin must be a real directory.
	info, err := os.Lstat(filepath.Join(staged, ".claude-plugin"))
	if err != nil {
		t.Fatalf(".claude-plugin missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf(".claude-plugin should be a real directory, got symlink")
	}
	if !info.IsDir() {
		t.Errorf(".claude-plugin should be a directory")
	}

	// .claude-plugin/marketplace.json is symlinked.
	markInfo, err := os.Lstat(filepath.Join(staged, ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatalf("marketplace.json missing: %v", err)
	}
	if markInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf(".claude-plugin/marketplace.json should be a symlink")
	}

	// .claude-plugin/plugin.json is a real file with compiled contents.
	pjInfo, err := os.Lstat(filepath.Join(staged, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("plugin.json missing: %v", err)
	}
	if pjInfo.Mode()&os.ModeSymlink != 0 {
		t.Errorf("plugin.json should be a real file, got symlink")
	}

	pjData, err := os.ReadFile(filepath.Join(staged, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("reading staged plugin.json: %v", err)
	}
	var pj map[string]any
	if err := json.Unmarshal(pjData, &pj); err != nil {
		t.Fatalf("compiled plugin.json is not valid JSON: %v\n%s", err, pjData)
	}
	if _, present := pj["mcpServers"]; present {
		t.Errorf("mcpServers still present in compiled plugin.json: %s", pjData)
	}
	if pj["name"] != "demo" {
		t.Errorf("name field lost or changed: %v", pj["name"])
	}
}

func TestCompilePluginDir_MissingPluginJSON(t *testing.T) {
	source := t.TempDir()
	mustMkdir(t, filepath.Join(source, ".claude-plugin"))
	// no plugin.json

	_, err := CompilePluginDir(source)
	if err == nil {
		t.Fatal("expected error when plugin.json is missing, got nil")
	}
}

func TestCompilePluginDir_MissingClaudePluginDir(t *testing.T) {
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "clown.json"), `{}`)

	_, err := CompilePluginDir(source)
	if err == nil {
		t.Fatal("expected error when .claude-plugin is missing, got nil")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

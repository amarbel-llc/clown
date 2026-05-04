package pluginhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompilePluginManifest_StripsMcpServers(t *testing.T) {
	input := []byte(`{
  "name": "moxy",
  "mcpServers": {
    "moxy": {"type": "stdio", "command": "/usr/bin/moxy"}
  }
}`)

	out, had, err := CompilePluginManifest(input, CompileInputs{})
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

func TestCompilePluginManifest_InjectsServerEntries(t *testing.T) {
	input := []byte(`{
  "name": "moxy",
  "mcpServers": {
    "moxy": {"type": "stdio", "command": "/usr/bin/moxy"}
  }
}`)

	entries := map[string]MCPServerEntry{
		"moxy": {Type: "http", URL: "http://127.0.0.1:12345/mcp"},
	}
	out, had, err := CompilePluginManifest(input, CompileInputs{Servers: entries})
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
	servers, present := got["mcpServers"]
	if !present {
		t.Fatalf("mcpServers missing after injection: %s", out)
	}
	serversMap, ok := servers.(map[string]any)
	if !ok {
		t.Fatalf("mcpServers is not an object: %T", servers)
	}
	entry, ok := serversMap["moxy"]
	if !ok {
		t.Fatalf("mcpServers missing 'moxy' key: %s", out)
	}
	entryMap := entry.(map[string]any)
	if entryMap["type"] != "http" {
		t.Errorf("type = %v, want %q", entryMap["type"], "http")
	}
	if entryMap["url"] != "http://127.0.0.1:12345/mcp" {
		t.Errorf("url = %v, want %q", entryMap["url"], "http://127.0.0.1:12345/mcp")
	}
	if got["name"] != "moxy" {
		t.Errorf("name field lost or changed: %v", got["name"])
	}
}

func TestCompilePluginManifest_InjectsTimeout(t *testing.T) {
	input := []byte(`{
  "name": "moxy",
  "mcpServers": {
    "moxy": {"type": "stdio", "command": "/usr/bin/moxy"}
  }
}`)

	entries := map[string]MCPServerEntry{
		"moxy": {Type: "http", URL: "http://127.0.0.1:12345/mcp", Timeout: 86400000},
	}
	out, _, err := CompilePluginManifest(input, CompileInputs{Servers: entries})
	if err != nil {
		t.Fatalf("CompilePluginManifest: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	servers := got["mcpServers"].(map[string]any)
	entry := servers["moxy"].(map[string]any)
	timeout, ok := entry["timeout"]
	if !ok {
		t.Fatalf("timeout missing from compiled entry: %s", out)
	}
	if got, want := timeout.(float64), float64(86400000); got != want {
		t.Errorf("timeout = %v, want %v", got, want)
	}
}

func TestCompilePluginManifest_OmitsTimeoutWhenZero(t *testing.T) {
	input := []byte(`{
  "name": "moxy",
  "mcpServers": {
    "moxy": {"type": "stdio", "command": "/usr/bin/moxy"}
  }
}`)

	entries := map[string]MCPServerEntry{
		"moxy": {Type: "http", URL: "http://127.0.0.1:12345/mcp"},
	}
	out, _, err := CompilePluginManifest(input, CompileInputs{Servers: entries})
	if err != nil {
		t.Fatalf("CompilePluginManifest: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	servers := got["mcpServers"].(map[string]any)
	entry := servers["moxy"].(map[string]any)
	if _, present := entry["timeout"]; present {
		t.Errorf("timeout key present despite zero value: %s", out)
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

	out, _, err := CompilePluginManifest(input, CompileInputs{})
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

	out, had, err := CompilePluginManifest(input, CompileInputs{})
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
	_, _, err := CompilePluginManifest([]byte("not json"), CompileInputs{})
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

	staged, err := CompilePluginDir(source, CompileInputs{})
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

func TestCompilePluginDir_InjectsServerEntries(t *testing.T) {
	source := t.TempDir()

	mustMkdir(t, filepath.Join(source, ".claude-plugin"))
	mustWrite(t, filepath.Join(source, ".claude-plugin", "plugin.json"),
		`{"name":"demo","mcpServers":{"srv":{"command":"x"}},"skills":["skills/"]}`)

	entries := map[string]MCPServerEntry{
		"srv": {Type: "http", URL: "http://127.0.0.1:9999/mcp"},
	}
	staged, err := CompilePluginDir(source, CompileInputs{Servers: entries})
	if err != nil {
		t.Fatalf("CompilePluginDir: %v", err)
	}
	defer os.RemoveAll(staged)

	pjData, err := os.ReadFile(filepath.Join(staged, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("reading staged plugin.json: %v", err)
	}
	var pj map[string]any
	if err := json.Unmarshal(pjData, &pj); err != nil {
		t.Fatalf("compiled plugin.json is not valid JSON: %v\n%s", err, pjData)
	}
	servers, present := pj["mcpServers"]
	if !present {
		t.Fatalf("mcpServers missing after injection: %s", pjData)
	}
	serversMap := servers.(map[string]any)
	entry, ok := serversMap["srv"]
	if !ok {
		t.Fatalf("mcpServers missing 'srv' key: %s", pjData)
	}
	entryMap := entry.(map[string]any)
	if entryMap["type"] != "http" {
		t.Errorf("type = %v, want %q", entryMap["type"], "http")
	}
	if entryMap["url"] != "http://127.0.0.1:9999/mcp" {
		t.Errorf("url = %v, want %q", entryMap["url"], "http://127.0.0.1:9999/mcp")
	}
}

func TestCompilePluginDir_MissingPluginJSON(t *testing.T) {
	source := t.TempDir()
	mustMkdir(t, filepath.Join(source, ".claude-plugin"))
	// no plugin.json

	_, err := CompilePluginDir(source, CompileInputs{})
	if err == nil {
		t.Fatal("expected error when plugin.json is missing, got nil")
	}
}

func TestCompilePluginDir_MissingClaudePluginDir(t *testing.T) {
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "clown.json"), `{}`)

	_, err := CompilePluginDir(source, CompileInputs{})
	if err == nil {
		t.Fatal("expected error when .claude-plugin is missing, got nil")
	}
}

func TestCompilePluginManifest_InjectsMonitors(t *testing.T) {
	input := []byte(`{"name":"demo"}`)

	monitors := []MonitorDef{
		{Name: "errlog", Command: "tail -F /tmp/x", Description: "errors"},
		{Name: "deploy", Command: "${CLAUDE_PLUGIN_ROOT}/poll.sh", Description: "deploy", When: "on-skill-invoke:debug"},
	}
	out, _, err := CompilePluginManifest(input, CompileInputs{Monitors: monitors})
	if err != nil {
		t.Fatalf("CompilePluginManifest: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	raw, present := got["monitors"]
	if !present {
		t.Fatalf("monitors missing after injection: %s", out)
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("monitors is not an array: %T", raw)
	}
	if len(arr) != 2 {
		t.Fatalf("monitors len = %d, want 2", len(arr))
	}
	first := arr[0].(map[string]any)
	if first["name"] != "errlog" {
		t.Errorf("monitors[0].name = %v", first["name"])
	}
	if _, hasWhen := first["when"]; hasWhen {
		t.Errorf("monitors[0].when present despite empty value: %v", first["when"])
	}
	second := arr[1].(map[string]any)
	if second["when"] != "on-skill-invoke:debug" {
		t.Errorf("monitors[1].when = %v", second["when"])
	}
}

func TestCompilePluginManifest_RejectsMonitorsConflict(t *testing.T) {
	input := []byte(`{"name":"demo","monitors":[{"name":"x","command":"echo x","description":"d"}]}`)

	monitors := []MonitorDef{{Name: "y", Command: "echo y", Description: "d"}}
	_, _, err := CompilePluginManifest(input, CompileInputs{Monitors: monitors})
	if err == nil {
		t.Fatal("expected conflict error when both clown.json and plugin.json declare monitors")
	}
	if !strings.Contains(err.Error(), "monitors declared in both") {
		t.Errorf("err = %q, want conflict message", err.Error())
	}
}

func TestCompilePluginManifest_PreservesPluginJSONMonitors(t *testing.T) {
	input := []byte(`{"name":"demo","monitors":[{"name":"x","command":"echo x","description":"d"}]}`)

	out, _, err := CompilePluginManifest(input, CompileInputs{})
	if err != nil {
		t.Fatalf("CompilePluginManifest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	arr, ok := got["monitors"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("monitors not preserved: %s", out)
	}
	if arr[0].(map[string]any)["name"] != "x" {
		t.Errorf("monitors[0].name = %v, want x", arr[0])
	}
}

func TestCompilePluginDir_InjectsMonitors(t *testing.T) {
	source := t.TempDir()

	mustMkdir(t, filepath.Join(source, ".claude-plugin"))
	mustWrite(t, filepath.Join(source, ".claude-plugin", "plugin.json"),
		`{"name":"demo"}`)

	in := CompileInputs{
		Monitors: []MonitorDef{
			{Name: "errlog", Command: "tail -F /tmp/x", Description: "errors"},
		},
	}
	staged, err := CompilePluginDir(source, in)
	if err != nil {
		t.Fatalf("CompilePluginDir: %v", err)
	}
	defer os.RemoveAll(staged)

	pjData, err := os.ReadFile(filepath.Join(staged, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("reading staged plugin.json: %v", err)
	}
	var pj map[string]any
	if err := json.Unmarshal(pjData, &pj); err != nil {
		t.Fatalf("compiled plugin.json is not valid JSON: %v\n%s", err, pjData)
	}
	arr, ok := pj["monitors"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("monitors not injected into staged plugin.json: %s", pjData)
	}
	if arr[0].(map[string]any)["name"] != "errlog" {
		t.Errorf("monitors[0].name = %v, want errlog", arr[0])
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

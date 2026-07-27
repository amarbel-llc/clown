package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/pluginhost"
	"code.linenisgreat.com/clown/internal/profile"
)

func TestReadOpencodeLocalConfig_ParsesURLAndToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "clown"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "url = \"https://example.com/v1\"\ntoken = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "clown", "opencode.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readOpencodeLocalConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.URL != "https://example.com/v1" {
		t.Errorf("url: got %q", cfg.URL)
	}
	if cfg.Token != "secret" {
		t.Errorf("token: got %q", cfg.Token)
	}
}

func TestReadOpencodeLocalConfig_LegacyFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "juggler"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "url = \"https://legacy.example.com/v1\"\ntoken = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "juggler", "opencode.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readOpencodeLocalConfig()
	if err != nil {
		t.Fatalf("legacy fallback read: %v", err)
	}
	if cfg.URL != "https://legacy.example.com/v1" {
		t.Errorf("url: got %q", cfg.URL)
	}
}

func TestReadOpencodeLocalConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	_, err := readOpencodeLocalConfig()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadOpencodeLocalConfig_MissingURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "clown"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "token = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "clown", "opencode.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readOpencodeLocalConfig()
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected url required error, got: %v", err)
	}
}

func TestWriteOpencodeLocalConfigFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)

	path, err := userConfigWritePath("opencode.toml")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := writeOpencodeLocalConfigFile(path, "https://example.com/v1", "secret-token"); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := readOpencodeLocalConfig()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.URL != "https://example.com/v1" {
		t.Errorf("url: got %q", cfg.URL)
	}
	if cfg.Token != "secret-token" {
		t.Errorf("token: got %q", cfg.Token)
	}
}

func TestWriteOpencodeLocalConfigFile_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)

	cfgDir := filepath.Join(dir, ".config", "clown")
	if _, err := os.Stat(cfgDir); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing before write, got err=%v", cfgDir, err)
	}

	path, err := userConfigWritePath("opencode.toml")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := writeOpencodeLocalConfigFile(path, "https://example.com/v1", "tok"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(cfgDir); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

func TestWriteOpencodeLocalConfigFile_QuotesAreEscaped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)

	path, err := userConfigWritePath("opencode.toml")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	// %q produces a Go-quoted string. The hand-rolled reader strips the
	// outer "" but does not unescape \". Document the current behavior:
	// a token containing a literal " survives as the escape sequence.
	if err := writeOpencodeLocalConfigFile(path, "https://example.com/v1", `tok"with"quotes`); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := readOpencodeLocalConfig()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(cfg.Token, `tok`) {
		t.Errorf("token round-trip lost prefix: got %q", cfg.Token)
	}
}

func TestWriteOpencodeConfigFile_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	err := writeOpencodeConfigFile(path, "https://example.com/v1", "test-token", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "test-token") {
		t.Errorf("config does not contain token: %s", content)
	}
	if !strings.Contains(content, "https://example.com/v1") {
		t.Errorf("config does not contain url: %s", content)
	}
	if !strings.Contains(content, "gpt-4o") {
		t.Errorf("config does not contain model: %s", content)
	}
}

func TestWriteOpencodeConfigFile_WithProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	err := writeOpencodeConfigFile(path, "https://gw.example.com/v1", "tok-xyz", "gpt-4o", nil)
	if err != nil {
		t.Fatalf("writeOpencodeConfigFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "gpt-4o") {
		t.Errorf("config missing model: %s", content)
	}
	if !strings.Contains(content, "gw.example.com") {
		t.Errorf("config missing url: %s", content)
	}
}

func TestWriteOpencodeConfigFile_ModelOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	err := writeOpencodeConfigFile(path, "https://gw.example.com/v1", "tok-xyz", "my-custom-model", nil)
	if err != nil {
		t.Fatalf("writeOpencodeConfigFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "my-custom-model") {
		t.Errorf("config missing custom model: %s", content)
	}
	if strings.Contains(content, "\"gpt-4o\"") {
		t.Errorf("default model gpt-4o should not appear when overridden: %s", content)
	}
}

// Phase 0. Verified 2026-07-27 that without OPENCODE_DISABLE_PROJECT_CONFIG a
// repo-local opencode.json REPLACES a same-named entry in clown's mcp map, so
// any repository could silently repoint a clown-managed MCP server.
func TestOpencodeEnv_HermeticSuppressesProjectConfig(t *testing.T) {
	got := opencodeEnv("/tmp/x/opencode.json", true)
	want := []string{
		"OPENCODE_CONFIG=/tmp/x/opencode.json",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpencodeEnv_NonHermeticOmitsSuppression(t *testing.T) {
	got := opencodeEnv("/tmp/x/opencode.json", false)
	want := []string{"OPENCODE_CONFIG=/tmp/x/opencode.json"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v (rollback path must behave as before)", got, want)
	}
}

// readOpencodeConfigJSON parses the generated opencode.json into a generic map
// for shape assertions, mirroring crush_test.go's readCrushConfigJSON.
func readOpencodeConfigJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal opencode.json: %v", err)
	}
	return out
}

// opencode's McpRemoteConfig discriminator is the literal "remote"
// (packages/core/src/v1/config/mcp.ts) — NOT clown's internal "http". Emitting
// clown's type verbatim would fail opencode's schema validation.
func TestWriteOpencodeConfigFile_EmitsMCPRemoteEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	mcp := map[string]pluginhost.MCPServerEntry{
		"moxy__moxy": {Type: "http", URL: "http://127.0.0.1:5001/mcp", Timeout: 30000},
	}
	if err := writeOpencodeConfigFile(path, "https://gw.example.com/v1", "tok", "gpt-4o", mcp); err != nil {
		t.Fatalf("writeOpencodeConfigFile: %v", err)
	}
	cfg := readOpencodeConfigJSON(t, path)

	servers, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp block missing or wrong type: %T", cfg["mcp"])
	}
	entry, ok := servers["moxy__moxy"].(map[string]any)
	if !ok {
		t.Fatalf("moxy__moxy entry missing: %v", servers)
	}
	if entry["type"] != "remote" {
		t.Errorf(`type = %v, want "remote" (opencode's literal, not clown's %q)`, entry["type"], "http")
	}
	if entry["url"] != "http://127.0.0.1:5001/mcp" {
		t.Errorf("url = %v", entry["url"])
	}
	if entry["enabled"] != true {
		t.Errorf("enabled = %v, want true", entry["enabled"])
	}
	// opencode's default is 5000ms, SHORTER than clown's 30s plugin default, so
	// an omitted timeout would kill long-running MCP tools at five seconds.
	if entry["timeout"] != float64(30000) {
		t.Errorf("timeout = %v, want 30000 (ms)", entry["timeout"])
	}
}

func TestWriteOpencodeConfigFile_OmitsMCPWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := writeOpencodeConfigFile(path, "u", "t", "m", nil); err != nil {
		t.Fatalf("writeOpencodeConfigFile: %v", err)
	}
	cfg := readOpencodeConfigJSON(t, path)
	if _, present := cfg["mcp"]; present {
		t.Errorf("no servers must emit no mcp key at all, got %v", cfg["mcp"])
	}
}

// A plugin that declares no timeout must inherit opencode's own default rather
// than being pinned to an explicit zero.
func TestWriteOpencodeConfigFile_OmitsZeroTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	mcp := map[string]pluginhost.MCPServerEntry{
		"p__s": {Type: "http", URL: "http://127.0.0.1:1/mcp", Timeout: 0},
	}
	if err := writeOpencodeConfigFile(path, "u", "t", "m", mcp); err != nil {
		t.Fatalf("writeOpencodeConfigFile: %v", err)
	}
	entry := readOpencodeConfigJSON(t, path)["mcp"].(map[string]any)["p__s"].(map[string]any)
	if _, present := entry["timeout"]; present {
		t.Errorf("zero timeout must be omitted, got %v", entry["timeout"])
	}
}

// TestResolveOpencodeGateway_ExpandsTokenEnvVar guards a real gap this
// change fixes: a profileTemplates-style Token like "${OPENROUTER_API_KEY}"
// must resolve to the env var's value, not be sent to opencode as the
// literal "${...}" string.
func TestResolveOpencodeGateway_ExpandsTokenEnvVar(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-real-key")
	prof := &profile.Profile{
		Provider: "opencode", Backend: "gateway",
		URL: "https://openrouter.ai/api/v1", Token: "${OPENROUTER_API_KEY}",
		Model: "openai/gpt-4o",
	}
	url, token, model := resolveOpencodeGateway(prof)
	if url != "https://openrouter.ai/api/v1" {
		t.Errorf("url = %q", url)
	}
	if token != "sk-real-key" {
		t.Errorf("token = %q, want expanded env value", token)
	}
	if model != "openai/gpt-4o" {
		t.Errorf("model = %q", model)
	}
}

// TestResolveOpencodeGateway_OpenrouterHardcodesURL guards the Phase B
// dispatch: provider=openrouter always uses openrouterGatewayURL, ignoring
// any profile.URL (which the openrouter template leaves empty anyway).
func TestResolveOpencodeGateway_OpenrouterHardcodesURL(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-real-key")
	prof := &profile.Profile{
		Provider: "openrouter", Backend: "gateway",
		URL: "https://should-be-ignored.example.com", Token: "${OPENROUTER_API_KEY}",
		Model: "openai/gpt-4o",
	}
	url, token, _ := resolveOpencodeGateway(prof)
	if url != openrouterGatewayURL {
		t.Errorf("url = %q, want hardcoded %q", url, openrouterGatewayURL)
	}
	if token != "sk-real-key" {
		t.Errorf("token = %q, want expanded env value", token)
	}
}

func TestWriteOpencodeConfigFile_SlashModelSlug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	const slug = "openai/gpt-4o"
	if err := writeOpencodeConfigFile(path, "https://openrouter.ai/api/v1", "key", slug, nil); err != nil {
		t.Fatalf("writeOpencodeConfigFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	// map key and "name" field must carry the full slug
	if !strings.Contains(content, `"openai/gpt-4o"`) {
		t.Errorf("slug missing from config: %s", content)
	}
	// model reference must be custom/<slug>
	if !strings.Contains(content, `"model":"custom/openai/gpt-4o"`) {
		t.Errorf("model ref wrong in config: %s", content)
	}
}

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

// TestResolveCrushGateway_ExpandsTokenEnvVar guards the same gap as
// opencode_test.go's TestResolveOpencodeGateway_ExpandsTokenEnvVar: a
// gateway profile's Token like "${OPENROUTER_API_KEY}" must resolve to the
// env var's value, not be sent to crush as the literal "${...}" string.
func TestResolveCrushGateway_ExpandsTokenEnvVar(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-real-key")
	prof := &profile.Profile{
		Provider: "crush", Backend: "gateway",
		URL: "https://gw.example.com/v1", Token: "${OPENROUTER_API_KEY}",
		Model: "gpt-4o",
	}
	baseURL, apiKey, model := resolveCrushGateway(prof)
	if baseURL != "https://gw.example.com/v1" {
		t.Errorf("baseURL = %q", baseURL)
	}
	if apiKey != "sk-real-key" {
		t.Errorf("apiKey = %q, want expanded env value", apiKey)
	}
	if model != "gpt-4o" {
		t.Errorf("model = %q", model)
	}
}

func TestReadCrushLocalConfig_ParsesURLAndToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "clown"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "url = \"https://example.com/v1\"\ntoken = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "clown", "crush.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readCrushLocalConfig()
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

func TestReadCrushLocalConfig_LegacyFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "juggler"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "url = \"https://legacy.example.com/v1\"\ntoken = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "juggler", "crush.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readCrushLocalConfig()
	if err != nil {
		t.Fatalf("legacy fallback read: %v", err)
	}
	if cfg.URL != "https://legacy.example.com/v1" {
		t.Errorf("url: got %q", cfg.URL)
	}
}

func TestReadCrushLocalConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	_, err := readCrushLocalConfig()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadCrushLocalConfig_MissingURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "clown"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "token = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "clown", "crush.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readCrushLocalConfig()
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected url required error, got: %v", err)
	}
}

func TestWriteCrushLocalConfigFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)

	path, err := userConfigWritePath("crush.toml")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := writeCrushLocalConfigFile(path, "https://example.com/v1", "secret-token"); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := readCrushLocalConfig()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cfg.URL != "https://example.com/v1" || cfg.Token != "secret-token" {
		t.Errorf("round trip mismatch: %+v", cfg)
	}
}

// readCrushConfigJSON parses <dir>/crush.json into a generic map for
// shape assertions. Uses untyped decoding because the test only cares
// about a few keys, not the full crush schema.
func readCrushConfigJSON(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "crush.json"))
	if err != nil {
		t.Fatalf("read crush.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal crush.json: %v", err)
	}
	return out
}

// --data-dir is where crush keeps SESSIONS as well as the workspace config, so
// instability here would silently break `crush --continue` on every launch.
func TestCrushDataDir_StableAcrossCalls(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := crushDataDir("/home/u/proj")
	if err != nil {
		t.Fatalf("crushDataDir: %v", err)
	}
	b, err := crushDataDir("/home/u/proj")
	if err != nil {
		t.Fatalf("crushDataDir: %v", err)
	}
	if a != b {
		t.Errorf("data dir must be stable for a project: %q vs %q", a, b)
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("data dir not created: %v", err)
	}
}

// crush's own default resolves a per-project .crush, so pooling every project
// into one directory would mix their session histories.
func TestCrushDataDir_DistinctPerProject(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, err := crushDataDir("/home/u/proj-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := crushDataDir("/home/u/proj-b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("distinct projects must not share a data dir: %q", a)
	}
}

func TestCrushArgs(t *testing.T) {
	base := []string{"--yolo"}
	if got := crushArgs(base, ""); !slices.Equal(got, base) {
		t.Errorf("hermeticity off must leave argv untouched: %v", got)
	}
	got := crushArgs(base, "/state/clown/crush/ab12")
	want := []string{"--data-dir", "/state/clown/crush/ab12", "--yolo"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The trap this whole helper exists for: crush stores MCP timeouts in SECONDS
// (internal/config/config.go), while clown and opencode use milliseconds.
// Copying clown's 30000 across verbatim would configure an 8-hour timeout.
func TestCrushMCPTimeoutSeconds(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want int
	}{
		{0, 0},      // unset stays unset so crush applies its own 15s default
		{-5, 0},     // defensive: never emit a negative
		{30000, 30}, // the common case
		{1500, 2},   // rounds UP; truncating would under-set the timeout
		{1, 1},      // sub-second floors at 1, never 0 (0 reads as "unset")
		{999, 1},
		{1000, 1},
	} {
		if got := crushMCPTimeoutSeconds(tc.ms); got != tc.want {
			t.Errorf("crushMCPTimeoutSeconds(%d) = %d, want %d", tc.ms, got, tc.want)
		}
	}
}

func TestWriteCrushConfig_EmitsMCPEntries(t *testing.T) {
	dir := t.TempDir()
	mcp := map[string]pluginhost.MCPServerEntry{
		"moxy__moxy": {Type: "http", URL: "http://127.0.0.1:5001/mcp", Timeout: 30000},
	}
	if err := writeCrushConfig(dir, crushBackendOpenAICompat, "u", "k", "m", mcp); err != nil {
		t.Fatalf("writeCrushConfig: %v", err)
	}
	cfg := readCrushConfigJSON(t, dir)

	servers, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp block missing or wrong type: %T", cfg["mcp"])
	}
	entry, ok := servers["moxy__moxy"].(map[string]any)
	if !ok {
		t.Fatalf("moxy__moxy entry missing: %v", servers)
	}
	if entry["type"] != "http" {
		t.Errorf("type = %v, want http", entry["type"])
	}
	if entry["url"] != "http://127.0.0.1:5001/mcp" {
		t.Errorf("url = %v", entry["url"])
	}
	if entry["timeout"] != float64(30) {
		t.Errorf("timeout = %v, want 30 SECONDS (clown stores 30000 ms)", entry["timeout"])
	}
}

func TestWriteCrushConfig_OmitsMCPWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := writeCrushConfig(dir, crushBackendOpenAICompat, "u", "k", "m", nil); err != nil {
		t.Fatalf("writeCrushConfig: %v", err)
	}
	if _, present := readCrushConfigJSON(t, dir)["mcp"]; present {
		t.Error("no servers must emit no mcp key at all")
	}
}

func TestWriteCrushConfig_OpenAICompat(t *testing.T) {
	dir := t.TempDir()
	if err := writeCrushConfig(dir, crushBackendOpenAICompat, "https://gw.example.com/v1", "tok-xyz", "qwen3-coder", nil); err != nil {
		t.Fatalf("writeCrushConfig: %v", err)
	}
	cfg := readCrushConfigJSON(t, dir)

	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		t.Fatalf("providers missing or wrong type: %T", cfg["providers"])
	}
	custom, ok := providers["custom"].(map[string]any)
	if !ok {
		t.Fatalf("custom provider missing: %v", providers)
	}
	if custom["type"] != "openai-compat" {
		t.Errorf("expected type=openai-compat, got %v", custom["type"])
	}
	if custom["base_url"] != "https://gw.example.com/v1" {
		t.Errorf("base_url: got %v", custom["base_url"])
	}
	if custom["api_key"] != "tok-xyz" {
		t.Errorf("api_key: got %v", custom["api_key"])
	}

	// Models must select the custom provider for both large and small.
	models, ok := cfg["models"].(map[string]any)
	if !ok {
		t.Fatalf("models map missing: %T", cfg["models"])
	}
	for _, slot := range []string{"large", "small"} {
		entry, ok := models[slot].(map[string]any)
		if !ok {
			t.Fatalf("models.%s missing", slot)
		}
		if entry["provider"] != "custom" {
			t.Errorf("models.%s.provider: got %v", slot, entry["provider"])
		}
		if entry["model"] != "qwen3-coder" {
			t.Errorf("models.%s.model: got %v", slot, entry["model"])
		}
	}

	opts, ok := cfg["options"].(map[string]any)
	if !ok {
		t.Fatalf("options missing")
	}
	if opts["disable_provider_auto_update"] != true {
		t.Errorf("expected disable_provider_auto_update=true, got %v", opts["disable_provider_auto_update"])
	}
}

func TestWriteCrushConfig_Anthropic(t *testing.T) {
	dir := t.TempDir()
	if err := writeCrushConfig(dir, crushBackendAnthropic, "", "", "claude-sonnet-4-5", nil); err != nil {
		t.Fatalf("writeCrushConfig: %v", err)
	}
	cfg := readCrushConfigJSON(t, dir)

	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		t.Fatalf("providers missing: %T", cfg["providers"])
	}
	anth, ok := providers["anthropic"].(map[string]any)
	if !ok {
		t.Fatalf("anthropic provider missing: %v", providers)
	}
	if anth["type"] != "anthropic" {
		t.Errorf("expected type=anthropic, got %v", anth["type"])
	}
	// API key is the env-var template; crush resolves it at runtime.
	if anth["api_key"] != "$ANTHROPIC_API_KEY" {
		t.Errorf("api_key: got %v", anth["api_key"])
	}
	// No base_url for anthropic — crush uses its builtin endpoint.
	if v, present := anth["base_url"]; present && v != "" {
		t.Errorf("base_url should be empty for anthropic, got %v", v)
	}

	models, _ := cfg["models"].(map[string]any)
	for _, slot := range []string{"large", "small"} {
		entry, ok := models[slot].(map[string]any)
		if !ok {
			t.Fatalf("models.%s missing", slot)
		}
		if entry["provider"] != "anthropic" {
			t.Errorf("models.%s.provider: got %v", slot, entry["provider"])
		}
	}
}

func TestWriteCrushConfig_DefaultModels(t *testing.T) {
	t.Run("openai-compat default", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeCrushConfig(dir, crushBackendOpenAICompat, "u", "k", "", nil); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "crush.json"))
		if !strings.Contains(string(data), "gpt-4o") {
			t.Errorf("expected default gpt-4o for openai-compat: %s", data)
		}
	})
	t.Run("anthropic default", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeCrushConfig(dir, crushBackendAnthropic, "", "", "", nil); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "crush.json"))
		if !strings.Contains(string(data), "claude-sonnet-4-5") {
			t.Errorf("expected default claude-sonnet-4-5 for anthropic: %s", data)
		}
	})
}

func TestWriteCrushConfig_UnknownBackendFails(t *testing.T) {
	dir := t.TempDir()
	err := writeCrushConfig(dir, crushBackend("nope"), "", "", "", nil)
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

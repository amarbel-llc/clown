package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCrushLocalConfig_ParsesURLAndToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "circus"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "url = \"https://example.com/v1\"\ntoken = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "circus", "crush.toml"), []byte(content), 0o600); err != nil {
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

func TestReadCrushLocalConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_, err := readCrushLocalConfig()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadCrushLocalConfig_MissingURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "circus"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "token = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "circus", "crush.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readCrushLocalConfig()
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected url required error, got: %v", err)
	}
}

func TestWriteCrushLocalConfigFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path, err := crushLocalConfigPath()
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

func TestWriteCrushConfig_OpenAICompat(t *testing.T) {
	dir := t.TempDir()
	if err := writeCrushConfig(dir, crushBackendOpenAICompat, "https://gw.example.com/v1", "tok-xyz", "qwen3-coder"); err != nil {
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
	if err := writeCrushConfig(dir, crushBackendAnthropic, "", "", "claude-sonnet-4-5"); err != nil {
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
		if err := writeCrushConfig(dir, crushBackendOpenAICompat, "u", "k", ""); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "crush.json"))
		if !strings.Contains(string(data), "gpt-4o") {
			t.Errorf("expected default gpt-4o for openai-compat: %s", data)
		}
	})
	t.Run("anthropic default", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeCrushConfig(dir, crushBackendAnthropic, "", "", ""); err != nil {
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
	err := writeCrushConfig(dir, crushBackend("nope"), "", "", "")
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

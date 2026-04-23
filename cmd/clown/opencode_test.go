package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOpencodeLocalConfig_ParsesURLAndToken(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "opencode.toml")
	content := "url = \"https://example.com/v1\"\ntoken = \"secret\"\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "circus"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".config", "circus", "opencode.toml"), []byte(content), 0o600); err != nil {
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

func TestReadOpencodeLocalConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_, err := readOpencodeLocalConfig()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadOpencodeLocalConfig_MissingURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".config", "circus"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "token = \"secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".config", "circus", "opencode.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readOpencodeLocalConfig()
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected url required error, got: %v", err)
	}
}

func TestWriteOpencodeConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	err := writeOpencodeConfig(dir, "https://example.com/v1", "test-token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
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

func TestWriteOpencodeConfig_WithProfile(t *testing.T) {
	dir := t.TempDir()
	err := writeOpencodeConfig(dir, "https://gw.example.com/v1", "tok-xyz", "gpt-4o")
	if err != nil {
		t.Fatalf("writeOpencodeConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
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

func TestWriteOpencodeConfig_ModelOverride(t *testing.T) {
	dir := t.TempDir()
	err := writeOpencodeConfig(dir, "https://gw.example.com/v1", "tok-xyz", "my-custom-model")
	if err != nil {
		t.Fatalf("writeOpencodeConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode", "opencode.json"))
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

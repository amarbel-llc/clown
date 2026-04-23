package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOpencodeConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	err := writeOpencodeConfig(dir, "test-token-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	path := filepath.Join(dir, "opencode", "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "test-token-value") {
		t.Errorf("config does not contain token: %s", content)
	}
	if !strings.Contains(content, "models.ag.genai-dev.gke.etsycloud.com") {
		t.Errorf("config does not contain gateway URL: %s", content)
	}
	if !strings.Contains(content, "claude-sonnet-4-6") {
		t.Errorf("config does not contain model: %s", content)
	}
}

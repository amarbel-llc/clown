package pluginhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadClownConfig(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version": 1,
		"httpServers": map[string]any{
			"test-server": map[string]any{
				"command":   "bin/server",
				"args":      []string{"--port", "0"},
				"transport": "sse",
				"healthcheck": map[string]any{
					"path":     "/health",
					"interval": "2s",
					"timeout":  "10s",
				},
			},
		},
	})

	cfg, err := LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
	srv, ok := cfg.HTTPServers["test-server"]
	if !ok {
		t.Fatal("missing test-server")
	}
	if srv.Command != "bin/server" {
		t.Errorf("command = %q, want %q", srv.Command, "bin/server")
	}
	if srv.Transport != "sse" {
		t.Errorf("transport = %q, want %q", srv.Transport, "sse")
	}
	if srv.Healthcheck.Path != "/health" {
		t.Errorf("healthcheck.path = %q, want %q", srv.Healthcheck.Path, "/health")
	}
	if srv.Healthcheck.Interval.Duration != 2*time.Second {
		t.Errorf("healthcheck.interval = %v, want 2s", srv.Healthcheck.Interval.Duration)
	}
	if srv.Healthcheck.Timeout.Duration != 10*time.Second {
		t.Errorf("healthcheck.timeout = %v, want 10s", srv.Healthcheck.Timeout.Duration)
	}
}

func TestLoadClownConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version": 1,
		"httpServers": map[string]any{
			"minimal": map[string]any{
				"command": "bin/srv",
			},
		},
	})

	cfg, err := LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := cfg.HTTPServers["minimal"]
	if srv.Transport != "streamable-http" {
		t.Errorf("transport = %q, want %q", srv.Transport, "streamable-http")
	}
	if srv.Healthcheck.Path != "/healthz" {
		t.Errorf("healthcheck.path = %q, want %q", srv.Healthcheck.Path, "/healthz")
	}
	if srv.Healthcheck.Interval.Duration != 1*time.Second {
		t.Errorf("healthcheck.interval = %v, want 1s", srv.Healthcheck.Interval.Duration)
	}
	if srv.Healthcheck.Timeout.Duration != 30*time.Second {
		t.Errorf("healthcheck.timeout = %v, want 30s", srv.Healthcheck.Timeout.Duration)
	}
}

func TestLoadClownConfigBadVersion(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version":     2,
		"httpServers": map[string]any{},
	})

	_, err := LoadClownConfig(dir)
	if err == nil {
		t.Fatal("expected error for version 2")
	}
}

func TestLoadClownConfigMissing(t *testing.T) {
	_, err := LoadClownConfig(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing clown.json")
	}
}

func TestPluginName(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(pluginDir, "plugin.json"), map[string]any{
		"name":    "test-plugin",
		"version": "1.0.0",
	})

	name, err := PluginName(dir)
	if err != nil {
		t.Fatal(err)
	}
	if name != "test-plugin" {
		t.Errorf("name = %q, want %q", name, "test-plugin")
	}
}

func TestGenerateMCPConfig(t *testing.T) {
	data, err := GenerateMCPConfig(map[string]MCPServerEntry{
		"moxy/my-server": {Type: "http", URL: "http://127.0.0.1:12345/mcp"},
		"moxy/sse-srv":   {Type: "sse", URL: "http://127.0.0.1:12346/sse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg MCPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg.MCPServers["moxy/my-server"]
	if !ok {
		t.Fatal("missing moxy/my-server")
	}
	if entry.Type != "http" {
		t.Errorf("type = %q, want %q", entry.Type, "http")
	}
	if entry.URL != "http://127.0.0.1:12345/mcp" {
		t.Errorf("url = %q, want %q", entry.URL, "http://127.0.0.1:12345/mcp")
	}

	// Claude-code's MCP schema requires a type discriminator alongside
	// the URL for HTTP-transport servers. Check the wire format
	// explicitly so this doesn't silently drift out of compliance again.
	if !strings.Contains(string(data), `"type": "http"`) {
		t.Errorf("generated JSON missing type=http:\n%s", data)
	}
	if !strings.Contains(string(data), `"type": "sse"`) {
		t.Errorf("generated JSON missing type=sse:\n%s", data)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

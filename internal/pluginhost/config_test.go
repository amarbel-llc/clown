package pluginhost

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestLoadClownConfigTimeout(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version": 1,
		"httpServers": map[string]any{
			"slow-server": map[string]any{
				"command": "bin/server",
				"timeout": 86400000,
			},
		},
	})

	cfg, err := LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := cfg.HTTPServers["slow-server"]
	if srv.Timeout != 86400000 {
		t.Errorf("timeout = %d, want 86400000", srv.Timeout)
	}
}

func TestLoadClownConfigTimeoutDefaultsToZero(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version": 1,
		"httpServers": map[string]any{
			"default-server": map[string]any{
				"command": "bin/server",
			},
		},
	})

	cfg, err := LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := cfg.HTTPServers["default-server"]
	if srv.Timeout != 0 {
		t.Errorf("timeout = %d, want 0 (unset)", srv.Timeout)
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

func TestDesugarStdioServers(t *testing.T) {
	cfg := &ClownConfig{
		Version: 1,
		StdioServers: map[string]StdioServerDef{
			"kagi": {
				Command: "kagi-mcp",
				Args:    []string{"--api-key-env", "KAGI_KEY"},
				Env:     map[string]string{"LOG_LEVEL": "info"},
				Timeout: 86400000,
			},
		},
	}

	if err := Desugar(cfg, "/usr/bin/clown-stdio-bridge"); err != nil {
		t.Fatalf("Desugar: %v", err)
	}

	if len(cfg.StdioServers) != 0 {
		t.Errorf("StdioServers should be cleared after Desugar, got %d entries", len(cfg.StdioServers))
	}

	srv, ok := cfg.HTTPServers["kagi"]
	if !ok {
		t.Fatalf("expected synthesized HTTPServers entry %q, got %#v", "kagi", cfg.HTTPServers)
	}
	if srv.Command != "/usr/bin/clown-stdio-bridge" {
		t.Errorf("command = %q, want %q", srv.Command, "/usr/bin/clown-stdio-bridge")
	}
	wantArgs := []string{"--command", "kagi-mcp", "--", "--api-key-env", "KAGI_KEY"}
	if !slicesEqual(srv.Args, wantArgs) {
		t.Errorf("args = %v, want %v", srv.Args, wantArgs)
	}
	if srv.Transport != "streamable-http" {
		t.Errorf("transport = %q, want %q", srv.Transport, "streamable-http")
	}
	if srv.Timeout != 86400000 {
		t.Errorf("timeout = %d, want 86400000", srv.Timeout)
	}
	if srv.Env["LOG_LEVEL"] != "info" {
		t.Errorf("env[LOG_LEVEL] = %q, want %q", srv.Env["LOG_LEVEL"], "info")
	}
	if srv.Healthcheck.Path != "/healthz" {
		t.Errorf("healthcheck.path = %q, want %q", srv.Healthcheck.Path, "/healthz")
	}
}

func TestDesugarNoArgs(t *testing.T) {
	cfg := &ClownConfig{
		Version: 1,
		StdioServers: map[string]StdioServerDef{
			"plain": {Command: "plain-mcp"},
		},
	}

	if err := Desugar(cfg, "/bridge"); err != nil {
		t.Fatalf("Desugar: %v", err)
	}

	srv := cfg.HTTPServers["plain"]
	wantArgs := []string{"--command", "plain-mcp", "--"}
	if !slicesEqual(srv.Args, wantArgs) {
		t.Errorf("args = %v, want %v (trailing -- with no wrapped args)", srv.Args, wantArgs)
	}
}

func TestDesugarNameCollision(t *testing.T) {
	cfg := &ClownConfig{
		Version: 1,
		HTTPServers: map[string]ServerDef{
			"shared": {Command: "/bin/http-server"},
		},
		StdioServers: map[string]StdioServerDef{
			"shared": {Command: "stdio-server"},
		},
	}

	err := Desugar(cfg, "/bridge")
	if err == nil {
		t.Fatal("expected error for name collision, got nil")
	}
}

func TestDesugarMissingBridgePath(t *testing.T) {
	cfg := &ClownConfig{
		Version: 1,
		StdioServers: map[string]StdioServerDef{
			"x": {Command: "x-mcp"},
		},
	}

	err := Desugar(cfg, "")
	if err == nil {
		t.Fatal("expected error for empty bridge path, got nil")
	}
}

func TestDesugarEmptyStdioServersIsNoop(t *testing.T) {
	cfg := &ClownConfig{
		Version: 1,
		HTTPServers: map[string]ServerDef{
			"keep": {Command: "/bin/http-server"},
		},
	}

	if err := Desugar(cfg, ""); err != nil {
		t.Fatalf("Desugar with no stdioServers should be a no-op even with empty bridge path: %v", err)
	}
	if _, ok := cfg.HTTPServers["keep"]; !ok {
		t.Errorf("existing httpServers entry was disturbed by Desugar")
	}
}

func TestLoadClownConfigStdioServers(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version": 1,
		"stdioServers": map[string]any{
			"kagi": map[string]any{
				"command": "kagi-mcp",
				"args":    []string{"--api-key-env", "KAGI_KEY"},
			},
		},
	})

	cfg, err := LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := cfg.StdioServers["kagi"]
	if !ok {
		t.Fatalf("expected stdioServers.kagi to be parsed, got %#v", cfg.StdioServers)
	}
	if srv.Command != "kagi-mcp" {
		t.Errorf("command = %q, want %q", srv.Command, "kagi-mcp")
	}
}

func slicesEqual[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

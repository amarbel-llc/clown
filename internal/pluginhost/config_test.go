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

	if err := Desugar(cfg, "/usr/bin/clown-stdio-bridge", ""); err != nil {
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

	if err := Desugar(cfg, "/bridge", ""); err != nil {
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

	err := Desugar(cfg, "/bridge", "")
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

	err := Desugar(cfg, "", "")
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

	if err := Desugar(cfg, "", ""); err != nil {
		t.Fatalf("Desugar with no stdioServers should be a no-op even with empty bridge path: %v", err)
	}
	if _, ok := cfg.HTTPServers["keep"]; !ok {
		t.Errorf("existing httpServers entry was disturbed by Desugar")
	}
}

// Regression: a stdio.Command that's plugin-relative (no leading `/`)
// must be absolutized against the plugin dir before going into the
// bridge's --command arg, so the bridge's exec.LookPath finds the
// binary regardless of its runtime CWD. See clown#36.
func TestDesugarAbsolutizesRelativeStdioCommand(t *testing.T) {
	cfg := &ClownConfig{
		Version: 1,
		StdioServers: map[string]StdioServerDef{
			"caldav": {Command: "bin/caldav", Args: []string{"--port", "0"}},
		},
	}

	if err := Desugar(cfg, "/bridge", "/plugins/caldav"); err != nil {
		t.Fatalf("Desugar: %v", err)
	}

	srv := cfg.HTTPServers["caldav"]
	wantArgs := []string{"--command", "/plugins/caldav/bin/caldav", "--", "--port", "0"}
	if !slicesEqual(srv.Args, wantArgs) {
		t.Errorf("args = %v, want %v", srv.Args, wantArgs)
	}
}

// Absolute stdio.Command values pass through unchanged regardless of
// pluginDir.
func TestDesugarLeavesAbsoluteStdioCommandAlone(t *testing.T) {
	cfg := &ClownConfig{
		Version: 1,
		StdioServers: map[string]StdioServerDef{
			"caldav": {Command: "/nix/store/abc-caldav/bin/caldav"},
		},
	}

	if err := Desugar(cfg, "/bridge", "/plugins/caldav"); err != nil {
		t.Fatalf("Desugar: %v", err)
	}

	srv := cfg.HTTPServers["caldav"]
	wantArgs := []string{"--command", "/nix/store/abc-caldav/bin/caldav", "--"}
	if !slicesEqual(srv.Args, wantArgs) {
		t.Errorf("args = %v, want %v", srv.Args, wantArgs)
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

func TestLoadClownConfig_Monitors(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version":     1,
		"httpServers": map[string]any{},
		"monitors": []any{
			map[string]any{
				"name":        "deploy-status",
				"command":     "${CLAUDE_PLUGIN_ROOT}/scripts/poll-deploy.sh",
				"description": "Deployment status changes",
			},
			map[string]any{
				"name":        "error-log",
				"command":     "tail -F ./logs/error.log",
				"description": "Application error log",
				"when":        "on-skill-invoke:debug",
			},
		},
	})

	cfg, err := LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Monitors) != 2 {
		t.Fatalf("monitors = %d, want 2", len(cfg.Monitors))
	}
	if cfg.Monitors[0].Name != "deploy-status" {
		t.Errorf("monitors[0].name = %q", cfg.Monitors[0].Name)
	}
	if cfg.Monitors[0].When != "" {
		t.Errorf("monitors[0].when = %q, want empty", cfg.Monitors[0].When)
	}
	if cfg.Monitors[1].When != "on-skill-invoke:debug" {
		t.Errorf("monitors[1].when = %q", cfg.Monitors[1].When)
	}
}

func TestLoadClownConfig_MonitorsOmitEmpty(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version":     1,
		"httpServers": map[string]any{},
	})

	cfg, err := LoadClownConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Monitors != nil {
		t.Errorf("monitors = %v, want nil", cfg.Monitors)
	}
}

func TestLoadClownConfig_MonitorsValidation(t *testing.T) {
	cases := []struct {
		name     string
		monitor  map[string]any
		wantSubs string
	}{
		{
			name:     "missing-name",
			monitor:  map[string]any{"command": "echo x", "description": "d"},
			wantSubs: "name is required",
		},
		{
			name:     "missing-command",
			monitor:  map[string]any{"name": "m", "description": "d"},
			wantSubs: "command is required",
		},
		{
			name:     "missing-description",
			monitor:  map[string]any{"name": "m", "command": "echo x"},
			wantSubs: "description is required",
		},
		{
			name:     "bad-when",
			monitor:  map[string]any{"name": "m", "command": "echo x", "description": "d", "when": "sometimes"},
			wantSubs: `when="sometimes"`,
		},
		{
			name:     "bad-skill-form",
			monitor:  map[string]any{"name": "m", "command": "echo x", "description": "d", "when": "on-skill-invoke:"},
			wantSubs: `when="on-skill-invoke:"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
				"version":  1,
				"monitors": []any{tc.monitor},
			})
			_, err := LoadClownConfig(dir)
			if err == nil {
				t.Fatalf("expected error matching %q, got nil", tc.wantSubs)
			}
			if !strings.Contains(err.Error(), tc.wantSubs) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSubs)
			}
		})
	}
}

func TestLoadClownConfig_MonitorsDuplicateName(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "clown.json"), map[string]any{
		"version": 1,
		"monitors": []any{
			map[string]any{"name": "dup", "command": "echo a", "description": "first"},
			map[string]any{"name": "dup", "command": "echo b", "description": "second"},
		},
	})
	_, err := LoadClownConfig(dir)
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
	if !strings.Contains(err.Error(), `duplicate name "dup"`) {
		t.Errorf("err = %q, want duplicate-name message", err.Error())
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

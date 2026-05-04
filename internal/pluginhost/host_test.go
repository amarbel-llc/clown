package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStartAllPartialFailure ensures StartAll returns a granular StartReport
// when a discovered server has a bad command: the server lands in Failed,
// no server lands in Started, and h.Servers stays empty.
func TestStartAllPartialFailure(t *testing.T) {
	dir := t.TempDir()

	bad := DiscoveredServer{
		PluginDir:  dir,
		PluginName: "test-plugin",
		ServerName: "bad-server",
		Def: ServerDef{
			Command: "/nonexistent/does-not-exist",
			Healthcheck: HealthcheckDef{
				Path:     "/healthz",
				Interval: JSONDuration{Duration: 50 * time.Millisecond},
				Timeout:  JSONDuration{Duration: 500 * time.Millisecond},
			},
		},
	}

	host := &Host{PluginDirs: []string{dir}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	report := host.StartAll(ctx, []DiscoveredServer{bad})

	if len(report.Started) != 0 {
		t.Errorf("expected no Started servers, got %d", len(report.Started))
	}
	if len(report.Failed) != 1 {
		t.Fatalf("expected 1 Failed entry, got %d", len(report.Failed))
	}
	if got := report.Failed[0].Server.Name(); got != "test-plugin/bad-server" {
		t.Errorf("Failed[0].Server.Name() = %q, want %q", got, "test-plugin/bad-server")
	}
	if report.Failed[0].Err == nil {
		t.Errorf("Failed[0].Err is nil")
	}
	if len(host.Servers) != 0 {
		t.Errorf("host.Servers populated on all-failure: %d entries", len(host.Servers))
	}

	host.Shutdown()
}

// TestDiscoveredServerName confirms the canonical name format used in logs.
func TestDiscoveredServerName(t *testing.T) {
	d := DiscoveredServer{PluginName: "alpha", ServerName: "beta"}
	if got := d.Name(); got != "alpha/beta" {
		t.Errorf("Name() = %q, want %q", got, "alpha/beta")
	}
}

// TestCompileForClaude_MonitorsOnlyPlugin covers the case where a plugin
// declares monitors but no MCP servers. Such a plugin produces no
// DiscoveredServer entries, so the union logic in CompileForClaude must
// pick it up via Host.monitorsByDir; otherwise its monitors would be
// silently dropped from the staged plugin.json.
func TestCompileForClaude_MonitorsOnlyPlugin(t *testing.T) {
	dir := t.TempDir()

	mustWrite(t, filepath.Join(dir, "clown.json"), `{
"version": 1,
"monitors": [
  {"name": "errlog", "command": "tail -F /tmp/x", "description": "errors"}
]
}`)
	mustMkdir(t, filepath.Join(dir, ".claude-plugin"))
	mustWrite(t, filepath.Join(dir, ".claude-plugin", "plugin.json"),
		`{"name": "monitors-only-demo"}`)

	host := &Host{PluginDirs: []string{dir}}
	defer host.Shutdown()

	discovered, err := host.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 0 {
		t.Errorf("expected zero DiscoveredServer entries for monitors-only plugin, got %d", len(discovered))
	}

	dirMap, err := host.CompileForClaude(discovered)
	if err != nil {
		t.Fatalf("CompileForClaude: %v", err)
	}
	staged, ok := dirMap[dir]
	if !ok {
		t.Fatalf("monitors-only plugin not staged; dirMap keys = %v", keysOf(dirMap))
	}

	pjData, err := os.ReadFile(filepath.Join(staged, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("reading staged plugin.json: %v", err)
	}
	var pj map[string]any
	if err := json.Unmarshal(pjData, &pj); err != nil {
		t.Fatalf("staged plugin.json is not valid JSON: %v\n%s", err, pjData)
	}
	arr, ok := pj["monitors"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("monitors not injected into monitors-only plugin: %s", pjData)
	}
	if arr[0].(map[string]any)["name"] != "errlog" {
		t.Errorf("monitors[0].name = %v, want errlog", arr[0])
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Sanity check: the discover → report roundtrip still works for a plugin
// dir that has no clown.json (discover returns nothing, StartAll on empty
// list returns an empty report).
func TestStartAllEmptyDiscovery(t *testing.T) {
	dir := t.TempDir()
	// Intentionally no clown.json.
	host := &Host{PluginDirs: []string{dir}}
	discovered, err := host.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 0 {
		t.Errorf("expected empty discovery for %s, got %d entries", filepath.Base(dir), len(discovered))
	}

	ctx := context.Background()
	report := host.StartAll(ctx, discovered)
	if len(report.Started) != 0 || len(report.Failed) != 0 {
		t.Errorf("expected empty report, got Started=%d Failed=%d", len(report.Started), len(report.Failed))
	}
}

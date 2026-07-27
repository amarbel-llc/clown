package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func TestSanitizeMCPKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"moxy", "moxy"},
		{"clown-builtin-jobs", "clown-builtin-jobs"},
		{"under_score", "under_score"},
		{"a/b", "a_b"},
		{"a.b c", "a_b_c"},
		{"plugin:name", "plugin_name"},
	} {
		if got := sanitizeMCPKey(tc.in); got != tc.want {
			t.Errorf("sanitizeMCPKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// managedFor builds a started-looking ManagedServer without spawning a process.
// handshake is unexported, so this only works from inside the package.
func managedFor(t *testing.T, name, addr string, timeoutMS int) *ManagedServer {
	t.Helper()
	return &ManagedServer{
		Name:      name,
		Def:       ServerDef{Timeout: timeoutMS},
		handshake: Handshake{Address: addr, Protocol: "streamable-http"},
	}
}

// ServerEntries is the accessor opencode/crush use: one flat map, not claude's
// per-plugin-dir namespacing. The key must be usable verbatim by BOTH providers'
// tool-name derivation — see the doc comment on ServerEntries.
func TestServerEntries_FlatSanitizedKeys(t *testing.T) {
	host := &Host{Servers: []*ManagedServer{
		managedFor(t, "moxy/moxy", "127.0.0.1:5001", 30000),
	}}
	discovered := []DiscoveredServer{{PluginName: "moxy", ServerName: "moxy"}}

	entries, err := host.ServerEntries(discovered)
	if err != nil {
		t.Fatalf("ServerEntries: %v", err)
	}
	e, ok := entries["moxy__moxy"]
	if !ok {
		t.Fatalf("want key moxy__moxy, got %v", keysOfEntries(entries))
	}
	if e.Type != "http" {
		t.Errorf("Type = %q, want http (streamable-http is mapped)", e.Type)
	}
	if e.URL != "http://127.0.0.1:5001/mcp" {
		t.Errorf("URL = %q", e.URL)
	}
	if e.Timeout != 30000 {
		t.Errorf("Timeout = %d, want 30000 (ms, untranslated at this layer)", e.Timeout)
	}
}

// A '/' in either component would be silently rewritten by opencode's sanitizer
// and would produce an invalid tool name under crush, so clown pre-sanitizes.
func TestServerEntries_SanitizesBothComponents(t *testing.T) {
	host := &Host{Servers: []*ManagedServer{
		managedFor(t, "we/ird/srv.name", "127.0.0.1:5002", 0),
	}}
	discovered := []DiscoveredServer{{PluginName: "we/ird", ServerName: "srv.name"}}

	entries, err := host.ServerEntries(discovered)
	if err != nil {
		t.Fatalf("ServerEntries: %v", err)
	}
	if _, ok := entries["we_ird__srv_name"]; !ok {
		t.Errorf("want key we_ird__srv_name, got %v", keysOfEntries(entries))
	}
}

// The important one: two servers whose sanitized keys coincide must be an
// error, not last-write-wins. Silent shadowing would make one plugin's tools
// vanish with no diagnostic at all.
func TestServerEntries_CollisionIsAnError(t *testing.T) {
	host := &Host{Servers: []*ManagedServer{
		managedFor(t, "a/b/c", "127.0.0.1:5001", 0),
		managedFor(t, "a.b/c", "127.0.0.1:5002", 0),
	}}
	discovered := []DiscoveredServer{
		{PluginName: "a/b", ServerName: "c"},
		{PluginName: "a.b", ServerName: "c"},
	}

	_, err := host.ServerEntries(discovered)
	if err == nil {
		t.Fatal("colliding keys must error rather than silently shadow")
	}
	if !strings.Contains(err.Error(), "a_b__c") {
		t.Errorf("error should name the colliding key, got: %v", err)
	}
}

// Servers that started but were never discovered (or vice versa) must not
// produce entries — mirrors serverEntriesByPluginDir's origin matching.
func TestServerEntries_IgnoresUnmatchedServers(t *testing.T) {
	host := &Host{Servers: []*ManagedServer{
		managedFor(t, "ghost/srv", "127.0.0.1:5001", 0),
	}}
	entries, err := host.ServerEntries([]DiscoveredServer{{PluginName: "other", ServerName: "srv"}})
	if err != nil {
		t.Fatalf("ServerEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("unmatched server produced entries: %v", keysOfEntries(entries))
	}
}

func keysOfEntries(m map[string]MCPServerEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

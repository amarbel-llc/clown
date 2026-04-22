package pluginhost

import (
	"context"
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

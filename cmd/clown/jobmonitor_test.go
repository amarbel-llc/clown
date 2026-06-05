package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobMonitorPluginDirSynthesized(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected a synthesized plugin dir when job-wakeup is enabled")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	manifestPath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading synthesized manifest: %v", err)
	}
	// Assert the TOP-LEVEL monitors array (matching pluginhost.compile.go's
	// doc["monitors"] and clown-json(5)); Claude Code reads monitors there,
	// NOT under an "experimental" wrapper.
	var m struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		Monitors []struct {
			Name        string `json:"name"`
			Command     string `json:"command"`
			Description string `json:"description"`
		} `json:"monitors"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, b)
	}
	// Guard against a regression to the experimental.monitors shape: the raw
	// JSON must not carry an "experimental" key at all.
	var rawDoc map[string]json.RawMessage
	if err := json.Unmarshal(b, &rawDoc); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, b)
	}
	if _, present := rawDoc["experimental"]; present {
		t.Fatalf("manifest must not nest monitors under experimental; got %s", b)
	}
	if _, present := rawDoc["monitors"]; !present {
		t.Fatalf("manifest must declare a top-level monitors array; got %s", b)
	}
	if len(m.Monitors) != 1 {
		t.Fatalf("want exactly one top-level monitor, got %d", len(m.Monitors))
	}
	mon := m.Monitors[0]
	if mon.Name != "clown-job-watch" {
		t.Fatalf("monitor name = %q, want clown-job-watch", mon.Name)
	}
	if mon.Description == "" {
		t.Fatal("monitor description must be non-empty")
	}
	if !filepath.IsAbs(strings.Fields(mon.Command)[0]) {
		t.Fatalf("monitor command = %q, want an absolute path (os.Executable resolves in go test)", mon.Command)
	}
	if !strings.HasSuffix(mon.Command, "job-watch") {
		t.Fatalf("monitor command = %q, want it to end in job-watch", mon.Command)
	}
}

func TestJobMonitorCommandIsAbsoluteWhenResolvable(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	// os.Executable() resolves to the test binary in `go test`, so the
	// command must be absolute in this environment.
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	cmd := jobWatchCommand()
	if !filepath.IsAbs(strings.Fields(cmd)[0]) {
		t.Fatalf("monitor command %q should start with an absolute path (os.Executable=%q)", cmd, exe)
	}
	if !strings.HasSuffix(cmd, " job-watch") {
		t.Fatalf("monitor command %q should end with the job-watch subcommand", cmd)
	}
}

func TestJobMonitorDisabledReturnsEmpty(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("expected no plugin dir when disabled, got %q", dir)
	}
}

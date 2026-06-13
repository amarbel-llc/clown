package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/buildcfg"
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
	if m.Name != "clown-builtin-jobs" {
		t.Fatalf("plugin name = %q, want clown-builtin-jobs", m.Name)
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

// When the stdio bridge is available, the synthesized built-in plugin also
// carries a clown.json declaring the job-mcp stdio server (RFC-0011 §1).
func TestJobMonitorPluginDirIncludesMCPWhenBridgeSet(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	orig := buildcfg.StdioBridgePath
	buildcfg.StdioBridgePath = "/nix/store/x/bin/clown-stdio-bridge"
	t.Cleanup(func() { buildcfg.StdioBridgePath = orig })

	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	b, err := os.ReadFile(filepath.Join(dir, "clown.json"))
	if err != nil {
		t.Fatalf("expected clown.json when bridge is set: %v", err)
	}
	var cfg struct {
		Version      int `json:"version"`
		StdioServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"stdioServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("clown.json invalid: %v\n%s", err, b)
	}
	if cfg.Version != 1 {
		t.Fatalf("clown.json version = %d, want 1", cfg.Version)
	}
	jobs, ok := cfg.StdioServers["jobs"]
	if !ok {
		t.Fatalf("clown.json missing stdioServers.jobs; got %s", b)
	}
	if !filepath.IsAbs(jobs.Command) {
		t.Fatalf("jobs.command = %q, want an absolute path", jobs.Command)
	}
	if len(jobs.Args) != 1 || jobs.Args[0] != "job-mcp" {
		t.Fatalf("jobs.args = %v, want [job-mcp]", jobs.Args)
	}
}

// When the clown-hook-allow path is baked in, the synthesized plugin ships a
// PreToolUse hook (hooks/hooks.json) wiring clown-hook-allow so the job tools
// auto-allow via the --plugin-dir mechanism (clown#130). The matcher is ".*"
// (clown-hook-allow decides per tool) and the command is the absolute baked
// path.
func TestJobMonitorPluginDirIncludesHookWhenHookAllowSet(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	orig := buildcfg.HookAllowPath
	buildcfg.HookAllowPath = "/nix/store/x/bin/clown-hook-allow"
	t.Cleanup(func() { buildcfg.HookAllowPath = orig })

	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	b, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("expected hooks/hooks.json when hook-allow path is set: %v", err)
	}
	var cfg struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("hooks.json invalid: %v\n%s", err, b)
	}
	if len(cfg.Hooks.PreToolUse) != 1 {
		t.Fatalf("want one PreToolUse entry, got %d; %s", len(cfg.Hooks.PreToolUse), b)
	}
	entry := cfg.Hooks.PreToolUse[0]
	if entry.Matcher != ".*" {
		t.Fatalf("matcher = %q, want .* (clown-hook-allow decides per tool)", entry.Matcher)
	}
	if len(entry.Hooks) != 1 || entry.Hooks[0].Type != "command" {
		t.Fatalf("want one command hook, got %+v", entry.Hooks)
	}
	if entry.Hooks[0].Command != buildcfg.HookAllowPath {
		t.Fatalf("command = %q, want the baked HookAllowPath %q", entry.Hooks[0].Command, buildcfg.HookAllowPath)
	}
}

// In dev builds (no hook-allow path) the PreToolUse hook is omitted; the tools
// prompt as before rather than shipping a hook pointing at a nonexistent path.
func TestJobMonitorPluginDirNoHookWhenHookAllowUnset(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	orig := buildcfg.HookAllowPath
	buildcfg.HookAllowPath = ""
	t.Cleanup(func() { buildcfg.HookAllowPath = orig })

	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if _, err := os.Stat(filepath.Join(dir, "hooks", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("hooks/hooks.json must be absent without a hook-allow path, stat err = %v", err)
	}
}

// In dev builds (no bridge path) the MCP server is omitted so host discovery's
// Desugar does not error and abort the launch; the monitor still ships.
func TestJobMonitorPluginDirNoMCPWhenBridgeUnset(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	orig := buildcfg.StdioBridgePath
	buildcfg.StdioBridgePath = ""
	t.Cleanup(func() { buildcfg.StdioBridgePath = orig })

	dir, err := synthJobMonitorPluginDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if _, err := os.Stat(filepath.Join(dir, "clown.json")); !os.IsNotExist(err) {
		t.Fatalf("clown.json must be absent without a bridge path, stat err = %v", err)
	}
}

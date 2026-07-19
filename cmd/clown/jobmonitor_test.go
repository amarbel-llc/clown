package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

func TestJobMonitorPluginDirSynthesized(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	// The monitor command is built from buildcfg.RingmasterPath (RFC-0015 §6),
	// which is empty in `go test`; set it so the absolute-path assertions hold.
	origRM := buildcfg.RingmasterPath
	buildcfg.RingmasterPath = "/nix/store/x/bin/ringmaster"
	t.Cleanup(func() { buildcfg.RingmasterPath = origRM })
	dir, err := synthJobMonitorPluginDir("sess-key-xyz")
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
	if mon.Name != "ringmaster-monitor" {
		t.Fatalf("monitor name = %q, want ringmaster-monitor", mon.Name)
	}
	if mon.Description == "" {
		t.Fatal("monitor description must be non-empty")
	}
	if !filepath.IsAbs(strings.Fields(mon.Command)[0]) {
		t.Fatalf("monitor command = %q, want an absolute path (buildcfg.RingmasterPath)", mon.Command)
	}
	if !strings.Contains(mon.Command, " monitor ") {
		t.Fatalf("monitor command = %q, want the ringmaster monitor subcommand", mon.Command)
	}
	// clown#136: the resolved per-instance key is baked in as --session so the
	// monitor watches the right channel without inheriting CLOWN_SESSION_ID.
	if !strings.HasSuffix(mon.Command, "--session sess-key-xyz") {
		t.Fatalf("monitor command = %q, want it to bake --session sess-key-xyz", mon.Command)
	}
}

func TestMonitorCommandUsesRingmasterPath(t *testing.T) {
	// With RingmasterPath baked in (nix builds), the monitor command is that
	// absolute path + the `monitor` subcommand (RFC-0015 §6).
	orig := buildcfg.RingmasterPath
	buildcfg.RingmasterPath = "/nix/store/x/bin/ringmaster"
	t.Cleanup(func() { buildcfg.RingmasterPath = orig })

	cmd := monitorCommand("")
	if !filepath.IsAbs(strings.Fields(cmd)[0]) {
		t.Fatalf("monitor command %q should start with the absolute RingmasterPath", cmd)
	}
	if !strings.HasSuffix(cmd, " monitor") {
		t.Fatalf("monitor command %q should end with the monitor subcommand (empty key omits --session)", cmd)
	}
	// A non-empty key is appended as --session (clown#136).
	if keyed := monitorCommand("k-1"); !strings.HasSuffix(keyed, " monitor --session k-1") {
		t.Fatalf("monitor command %q should bake --session for a non-empty key", keyed)
	}
}

// In dev builds (empty RingmasterPath) the command falls back to the bare
// `ringmaster monitor`, resolved via PATH (RFC-0015 §6).
func TestMonitorCommandBareFallback(t *testing.T) {
	orig := buildcfg.RingmasterPath
	buildcfg.RingmasterPath = ""
	t.Cleanup(func() { buildcfg.RingmasterPath = orig })
	if cmd := monitorCommand(""); cmd != "ringmaster monitor" {
		t.Fatalf("monitor command = %q, want bare `ringmaster monitor`", cmd)
	}
	if cmd := monitorCommand("k-1"); cmd != "ringmaster monitor --session k-1" {
		t.Fatalf("monitor command = %q, want bare command with --session", cmd)
	}
}

func TestTroupeAgentCommand(t *testing.T) {
	orig := buildcfg.TroupePath
	t.Cleanup(func() { buildcfg.TroupePath = orig })

	// Nix builds: absolute TroupePath + `agent`, with the session key threaded as
	// a scoped env prefix (clown#136-style: `troupe agent` reads
	// CLOWN_SESSION_ID, which clown does not export ambiently and takes no flag
	// for).
	buildcfg.TroupePath = "/nix/store/x/bin/troupe"
	cmd := troupeAgentCommand("k-1")
	if !strings.HasSuffix(cmd, "/nix/store/x/bin/troupe agent") {
		t.Fatalf("agent command = %q, want it to end with the absolute troupe agent subcommand", cmd)
	}
	if !strings.HasPrefix(cmd, "env CLOWN_SESSION_ID=k-1 ") {
		t.Fatalf("agent command = %q, want the scoped CLOWN_SESSION_ID env prefix", cmd)
	}
	// An empty key omits the prefix.
	if cmd := troupeAgentCommand(""); cmd != "/nix/store/x/bin/troupe agent" {
		t.Fatalf("agent command = %q, want the bare absolute agent command for an empty key", cmd)
	}
	// Dev build (empty TroupePath) falls back to the bare `troupe agent`.
	buildcfg.TroupePath = ""
	if cmd := troupeAgentCommand("k-1"); cmd != "env CLOWN_SESSION_ID=k-1 troupe agent" {
		t.Fatalf("agent command = %q, want the bare `troupe agent` with env prefix", cmd)
	}
}

// The troupe-agent monitor is registered only when the session opts into the
// xmpp transport (TROUPE_TRANSPORT=xmpp) and the troupe binary is available.
func TestJobMonitorTroupeAgentGatedOnXMPP(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	origRM, origTroupe := buildcfg.RingmasterPath, buildcfg.TroupePath
	buildcfg.RingmasterPath = "/nix/store/x/bin/ringmaster"
	buildcfg.TroupePath = "/nix/store/x/bin/troupe"
	t.Cleanup(func() { buildcfg.RingmasterPath, buildcfg.TroupePath = origRM, origTroupe })

	monitorNames := func(t *testing.T) []string {
		t.Helper()
		dir, err := synthJobMonitorPluginDir("sess-1")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Monitors []struct {
				Name string `json:"name"`
			} `json:"monitors"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		names := make([]string, len(m.Monitors))
		for i, mon := range m.Monitors {
			names[i] = mon.Name
		}
		return names
	}

	// Local (default): only the ringmaster monitor.
	t.Setenv("TROUPE_TRANSPORT", "local")
	if names := monitorNames(t); len(names) != 1 || names[0] != "ringmaster-monitor" {
		t.Fatalf("local transport monitors = %v, want just [ringmaster-monitor]", names)
	}

	// xmpp: the troupe agent is registered alongside the ringmaster monitor.
	t.Setenv("TROUPE_TRANSPORT", "xmpp")
	names := monitorNames(t)
	found := false
	for _, n := range names {
		if n == "troupe-agent" {
			found = true
		}
	}
	if len(names) != 2 || !found {
		t.Fatalf("xmpp transport monitors = %v, want ringmaster-monitor + troupe-agent", names)
	}
}

// providerUsesPluginDirs gates which providers get the synthesized job-monitor
// plugin dir (only --plugin-dir subprocess providers).
func TestProviderUsesPluginDirs(t *testing.T) {
	uses := map[string]bool{
		"claude":   true,
		"clownbox": true,
		"codex":    false,
		"juggler":  false,
		"opencode": false,
		"crush":    false,
	}
	for provider, want := range uses {
		if got := providerUsesPluginDirs(provider); got != want {
			t.Errorf("providerUsesPluginDirs(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestJobMonitorDisabledReturnsEmpty(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	dir, err := synthJobMonitorPluginDir("")
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
		t.Fatalf("expected no plugin dir when disabled, got %q", dir)
	}
}

// When the stdio bridge is available, the synthesized built-in plugin carries a
// clown.json declaring the job-mcp stdio servers, split into the troupe
// (messaging) and ringmaster (job control) surfaces (RFC-0011 §1, clown#144).
func TestJobMonitorPluginDirIncludesMCPWhenBridgeSet(t *testing.T) {
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
	// The MCP servers are synthesized only when the bridge AND both binary paths
	// are baked in (RFC-0015 §6); set all three.
	origBridge, origRM, origTroupe := buildcfg.StdioBridgePath, buildcfg.RingmasterPath, buildcfg.TroupePath
	buildcfg.StdioBridgePath = "/nix/store/x/bin/clown-stdio-bridge"
	buildcfg.RingmasterPath = "/nix/store/x/bin/ringmaster"
	buildcfg.TroupePath = "/nix/store/x/bin/troupe"
	t.Cleanup(func() {
		buildcfg.StdioBridgePath, buildcfg.RingmasterPath, buildcfg.TroupePath = origBridge, origRM, origTroupe
	})

	dir, err := synthJobMonitorPluginDir("")
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
			Command      string   `json:"command"`
			Args         []string `json:"args"`
			SystemPrompt bool     `json:"systemPrompt"`
		} `json:"stdioServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("clown.json invalid: %v\n%s", err, b)
	}
	if cfg.Version != 1 {
		t.Fatalf("clown.json version = %d, want 1", cfg.Version)
	}
	// The legacy single "jobs" server is gone; the split servers replace it.
	if _, present := cfg.StdioServers["jobs"]; present {
		t.Fatalf("clown.json must not declare the legacy 'jobs' server after the split; got %s", b)
	}
	for surface, want := range map[string]struct {
		args    []string
		command string
	}{
		"ringmaster": {args: []string{"mcp"}, command: buildcfg.RingmasterPath},
		"troupe":     {args: []string{"mcp"}, command: buildcfg.TroupePath},
	} {
		wantArgs := want.args
		srv, ok := cfg.StdioServers[surface]
		if !ok {
			t.Fatalf("clown.json missing stdioServers.%s; got %s", surface, b)
		}
		if srv.Command != want.command {
			t.Fatalf("%s.command = %q, want the baked path %q", surface, srv.Command, want.command)
		}
		if !filepath.IsAbs(srv.Command) {
			t.Fatalf("%s.command = %q, want an absolute path", surface, srv.Command)
		}
		if len(srv.Args) != len(wantArgs) {
			t.Fatalf("%s.args = %v, want %v", surface, srv.Args, wantArgs)
		}
		for i := range wantArgs {
			if srv.Args[i] != wantArgs[i] {
				t.Fatalf("%s.args = %v, want %v", surface, srv.Args, wantArgs)
			}
		}
	}
	// Both surfaces own a dynamic system-prompt fragment: ringmaster (job
	// platform) and troupe (messaging, since B.4 dropped ringmaster's chat
	// coverage). FetchPromptFragments collects from every opted-in server, so
	// both are appended.
	if !cfg.StdioServers["ringmaster"].SystemPrompt {
		t.Fatalf("ringmaster server must set systemPrompt:true; got %s", b)
	}
	if !cfg.StdioServers["troupe"].SystemPrompt {
		t.Fatalf("troupe server must set systemPrompt:true; got %s", b)
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

	dir, err := synthJobMonitorPluginDir("")
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

	dir, err := synthJobMonitorPluginDir("")
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

	dir, err := synthJobMonitorPluginDir("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if _, err := os.Stat(filepath.Join(dir, "clown.json")); !os.IsNotExist(err) {
		t.Fatalf("clown.json must be absent without a bridge path, stat err = %v", err)
	}
}

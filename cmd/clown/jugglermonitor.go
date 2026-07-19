package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

// jugglerMCPDisabled reports whether the juggler subagent-delegation tool is
// switched off via CLOWN_DISABLE_JUGGLER_MCP=1. When set, the synthesized
// plugin dir is not written so no juggler-prompt tool is registered.
func jugglerMCPDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_JUGGLER_MCP") == "1"
}

// jugglerPluginManifest is the minimal .claude-plugin/plugin.json this
// built-in plugin needs — no monitors, unlike clown-builtin-jobs.
type jugglerPluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// synthJugglerPluginDir writes a temporary built-in plugin directory
// declaring `juggler mcp` as a stdioServers entry (docs/plans/2026-07-11-
// juggler-subagent-tool-design.md), and returns its path. The caller
// appends the path to the --plugin-dir set passed to Claude and removes
// the directory on shutdown, mirroring synthJobMonitorPluginDir's contract
// exactly. Returns ("", nil) when disabled (CLOWN_DISABLE_JUGGLER_MCP=1) or
// when buildcfg.JugglerCliPath is empty (dev builds — go run/go build never
// burn this in; only the nix derivation does), so a dev build never ships
// a clown.json pointing at a nonexistent path.
func synthJugglerPluginDir() (string, error) {
	if jugglerMCPDisabled() || buildcfg.JugglerCliPath == "" {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "clown-juggler-plugin-")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	manifest := jugglerPluginManifest{Name: "clown-builtin-juggler", Version: "1"}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}

	clownCfg := map[string]any{
		"version": 1,
		"stdioServers": map[string]any{
			"juggler": map[string]any{
				"command": buildcfg.JugglerCliPath,
				"args":    []string{"mcp"},
			},
		},
	}
	cb, err := json.MarshalIndent(clownCfg, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "clown.json"), cb, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	// Deliberately NO hooks/hooks.json here: unlike clown-builtin-jobs, this
	// tool makes live calls to (potentially paid, third-party) APIs on the
	// agent's own initiative, so it must NOT auto-allow. clown-builtin-jobs'
	// own hooks.json (matcher ".*") already runs clown-hook-allow for every
	// tool call in the session including this plugin's, and clown-hook-allow
	// deliberately does not list this plugin's tool prefix in its allow-map
	// (see the comment following jobToolPrefix in cmd/clown-hook-allow/main.go)
	// — this plugin's tools (mcp__plugin_clown-builtin-juggler_...) fall
	// through to the existing defer-to-native-prompt path with zero new
	// hook code. If clown-builtin-jobs is ever disabled independently
	// (CLOWN_DISABLE_JOB_WAKEUP=1) while this plugin stays enabled, no hook
	// runs at all for this tool either — which still means "defer to Claude
	// Code's native prompt," the same outcome, just via a different path.
	return dir, nil
}

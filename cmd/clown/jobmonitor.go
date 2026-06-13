package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/amarbel-llc/clown/internal/buildcfg"
)

// jobMonitorPlugin is the synthesized built-in plugin manifest that registers
// the clown job-watch monitor as a Claude Code monitor. The monitors array is
// TOP-LEVEL in plugin.json (matching internal/pluginhost/compile.go, which
// injects doc["monitors"], and clown-json(5)); Claude Code reads monitors
// there, not under an "experimental" wrapper. Each stdout line the monitor
// emits becomes an agent notification (RFC-0009 §9).
type jobMonitorPlugin struct {
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Monitors []jobMonitorEntry `json:"monitors"`
}

// jobMonitorEntry mirrors pluginhost.MonitorDef's wire fields.
type jobMonitorEntry struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

// clownExePath returns the absolute path to the running clown binary, or ""
// if it cannot be resolved. It backs both the job-watch monitor command and
// the CLOWN_BIN env var exported for plugin producers, so the two always name
// the same binary.
func clownExePath() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return ""
}

// jobWatchCommand returns the monitor command string Claude Code spawns.
// Claude Code spawns monitors with the session PATH, on which `clown` may not
// appear; an absolute path from clownExePath() makes the monitor run regardless
// of PATH. When it cannot be resolved we fall back to the bare `clown
// job-watch`, which still works wherever clown is on PATH.
func jobWatchCommand() string {
	if exe := clownExePath(); exe != "" {
		return exe + " job-watch"
	}
	return "clown job-watch"
}

// providerUsesPluginDirs reports whether the provider consumes --plugin-dir
// (and runs as a subprocess so deferred cleanup fires). Only those need the
// synthesized job-watch monitor dir. claude and clownbox thread pluginDirs
// into runWithPluginHost (cmd.Run, not syscall.Exec); codex/opencode/crush
// never receive pluginDirs and codex/naked exec away, so a synthesized dir
// would leak. circus is a stub that ignores pluginDirs entirely.
func providerUsesPluginDirs(provider string) bool {
	switch provider {
	case "claude", "clownbox":
		return true
	default:
		return false
	}
}

// synthJobMonitorPluginDir writes a temporary built-in plugin directory whose
// .claude-plugin/plugin.json declares the clown job-watch monitor, and returns
// its path. The caller appends the path to the --plugin-dir set passed to
// Claude and removes the directory on shutdown. When CLOWN_DISABLE_JOB_WAKEUP=1
// it returns ("", nil) so the monitor is not registered (RFC-0009 §8).
func synthJobMonitorPluginDir() (string, error) {
	if jobWakeupDisabled() {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "clown-jobwake-plugin-")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	manifest := jobMonitorPlugin{
		Name:    "clown-builtin-jobs",
		Version: "1",
		Monitors: []jobMonitorEntry{{
			Name:        "clown-job-watch",
			Command:     jobWatchCommand(),
			Description: "clown job-wakeup channel: wakes this session when a background job finishes",
		}},
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}

	// When the stdio bridge is available (nix builds; empty in dev `go run`),
	// the same built-in plugin also serves the job-platform MCP tools
	// (RFC-0011): a clown.json stdioServers entry runs `clown job-mcp`, which
	// clown's own pluginhost Desugars through clown-stdio-bridge to
	// streamable-HTTP — clown self-consuming RFC-0002. Skipped in dev builds,
	// where Desugar errors without a bridge path and would abort the launch;
	// the monitor still works there. The command MUST be absolute (the
	// synthesized plugin dir holds no clown binary for Desugar to resolve a
	// relative command against), so a missing clownExePath() also skips it.
	if exe := clownExePath(); exe != "" && buildcfg.StdioBridgePath != "" {
		clownCfg := map[string]any{
			"version": 1,
			"stdioServers": map[string]any{
				"jobs": map[string]any{
					"command": exe,
					"args":    []string{"job-mcp"},
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
	}

	// When the clown-hook-allow binary path is baked in (nix builds), ship a
	// PreToolUse hook THROUGH THE PLUGIN so the job MCP tools auto-allow with no
	// permission prompt (clown#130). This is the live mechanism: claude loads a
	// plugin's hooks/hooks.json via --plugin-dir in every session — unlike
	// managed-settings, which it does not read outside --tent (clown#133). The
	// `.*` matcher routes every tool through clown-hook-allow, which returns
	// "allow" for the clown-builtin-jobs tool prefix and /nix/store reads and
	// "defer" otherwise, leaving all other permission decisions untouched.
	// Mirrors how spinclass and moxy auto-allow their own tools. Skipped in dev
	// builds (empty HookAllowPath), where the tools prompt as before.
	if buildcfg.HookAllowPath != "" {
		hooksDir := filepath.Join(dir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		hooksCfg := map[string]any{
			"hooks": map[string]any{
				"PreToolUse": []any{
					map[string]any{
						"matcher": ".*",
						"hooks": []any{
							map[string]any{
								"type":    "command",
								"command": buildcfg.HookAllowPath,
								"timeout": 5,
							},
						},
					},
				},
			},
		}
		hb, err := json.MarshalIndent(hooksCfg, "", "  ")
		if err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), hb, 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

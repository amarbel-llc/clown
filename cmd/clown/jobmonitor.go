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

// jobWakeupDisabled reports whether the job-wakeup facility is switched off via
// CLOWN_DISABLE_JOB_WAKEUP=1 (RFC-0009 §8). When set, the synthesized monitor
// plugin dir is not written so no monitor is registered.
func jobWakeupDisabled() bool {
	return os.Getenv("CLOWN_DISABLE_JOB_WAKEUP") == "1"
}

// clownExePath returns the absolute path to the running clown binary, or ""
// if it cannot be resolved. It backs the CLOWN_BIN env var exported for plugin
// producers and the attach re-exec path.
func clownExePath() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return ""
}

// monitorCommand returns the wake-monitor command string Claude Code spawns
// (RFC-0015 §6, formerly `clown job-watch`). The monitor now lives on the
// ringmaster binary — a separate Nix store output — so the path comes from
// buildcfg.RingmasterPath (baked at build time), not clown's own
// os.Executable(). Claude Code spawns monitors with the session PATH, on which
// `ringmaster` may not appear; the absolute burned-in path makes it run
// regardless of PATH. Dev builds leave RingmasterPath empty and fall back to the
// bare `ringmaster monitor`, which works wherever ringmaster is on PATH.
//
// key is the resolved per-instance channel key (RFC-0009 §2), baked in as
// `--session <key>` so the monitor learns its channel explicitly rather than by
// inheriting CLOWN_SESSION_ID from the ambient env (clown#136). Empty key omits
// the flag (the monitor then falls back to env resolution).
func monitorCommand(key string) string {
	base := "ringmaster monitor"
	if buildcfg.RingmasterPath != "" {
		base = buildcfg.RingmasterPath + " monitor"
	}
	if key != "" {
		return base + " --session " + key
	}
	return base
}

// troupeAgentCommand returns the `troupe agent` command string for the
// persistent per-session XMPP receiver, registered as a monitor when the
// session opts into the troupe xmpp transport (troupe RFC-0001 §2/§6). It is the
// RECEIVE half of the xmpp backend: it joins this session's MUC rooms and
// delivers inbound cross-host chat onto this host's local journal, then nudges
// this session's channel so the ringmaster monitor emits the wake.
//
// The absolute buildcfg.TroupePath is used for the same PATH-independence reason
// as the ringmaster monitor. The agent resolves its session key from
// CLOWN_SESSION_ID (jobwake.SessionKey()), which clown does NOT export ambiently
// (clown#136) and which `troupe agent` takes no flag for; it is threaded here as
// a SCOPED env prefix so the agent's own-channel nudge targets THIS session's
// channel (matching the ringmaster monitor's --session key) without polluting
// the claude subtree env. The transport coordinates (TROUPE_TRANSPORT +
// TROUPE_XMPP_*) and CLOWN_GROUP_ID are ambient (os.Setenv'd from the clownfile
// [messaging] table) and inherited.
func troupeAgentCommand(key string) string {
	base := "troupe agent"
	if buildcfg.TroupePath != "" {
		base = buildcfg.TroupePath + " agent"
	}
	if key != "" {
		base = "env CLOWN_SESSION_ID=" + key + " " + base
	}
	return base
}

// providerUsesPluginDirs reports whether the provider consumes --plugin-dir
// (and runs as a subprocess so deferred cleanup fires). Only those need the
// synthesized job-watch monitor dir. claude and clownbox thread pluginDirs
// into runWithPluginHost (cmd.Run, not syscall.Exec); codex/opencode/crush
// never receive pluginDirs and codex/naked exec away, so a synthesized dir
// would leak. juggler is a stub that ignores pluginDirs entirely.
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
func synthJobMonitorPluginDir(sessionKey string) (string, error) {
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
	monitors := []jobMonitorEntry{{
		Name:        "ringmaster-monitor",
		Command:     monitorCommand(sessionKey),
		Description: "clown job-wakeup channel: wakes this session when a background job finishes",
	}}
	// When the session opts into the troupe xmpp transport (clownfile
	// [messaging], exported as TROUPE_TRANSPORT=xmpp), also run the per-session
	// `troupe agent`: the persistent XMPP receiver that delivers cross-host chat
	// onto this host's local journal + nudges this session's channel (troupe
	// RFC-0001 §2/§6). It needs the troupe binary (nix builds); dev builds (empty
	// TroupePath) skip it, as they do the MCP servers below.
	if os.Getenv("TROUPE_TRANSPORT") == "xmpp" && buildcfg.TroupePath != "" {
		monitors = append(monitors, jobMonitorEntry{
			Name:        "troupe-agent",
			Command:     troupeAgentCommand(sessionKey),
			Description: "troupe XMPP messaging receiver: delivers cross-host chat into this session",
		})
	}
	manifest := jobMonitorPlugin{
		Name:     "clown-builtin-jobs",
		Version:  "1",
		Monitors: monitors,
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
	// (RFC-0011): two clown.json stdioServers entries run `ringmaster mcp` and
	// `troupe mcp` (RFC-0015 §6), which clown's own pluginhost Desugars through
	// clown-stdio-bridge to streamable-HTTP — clown self-consuming RFC-0002. The
	// surface split (clown#144) gives the agent two intent-revealing tool groups:
	// ringmaster (job control) and troupe (messaging), surfaced as
	// plugin:clown-builtin-jobs:ringmaster / :troupe. Skipped in dev builds,
	// where the binary paths are empty and Desugar would error without a bridge
	// path and abort the launch; the monitor still works there. The commands MUST
	// be absolute (the synthesized plugin dir holds no binary for Desugar to
	// resolve a relative command against), which the burned-in RingmasterPath /
	// TroupePath satisfy.
	if buildcfg.RingmasterPath != "" && buildcfg.TroupePath != "" && buildcfg.StdioBridgePath != "" {
		clownCfg := map[string]any{
			"version": 1,
			"stdioServers": map[string]any{
				// ringmaster: job lifecycle + status (clown#144). It owns the
				// dynamic system-prompt fragment covering the job platform: the
				// bridge serves /clown/system-prompt by issuing prompts/get to the
				// ringmaster mcp server, and clown folds the live fragment into
				// claude's append prompt (RFC-0002 §dynamic fragments).
				"ringmaster": map[string]any{
					"command":      buildcfg.RingmasterPath,
					"args":         []string{"mcp"},
					"systemPrompt": true,
				},
				// troupe: messaging — chat + the standalone waking job_message
				// (clown#144). Also owns a dynamic system-prompt fragment (its own
				// prompts/get, wire-identical to ringmaster's) covering chat
				// orientation, which ringmaster's fragment stopped carrying once it
				// shed its chat funcs (B.4). FetchPromptFragments collects from every
				// opted-in server, so both fragments are appended.
				"troupe": map[string]any{
					"command":      buildcfg.TroupePath,
					"args":         []string{"mcp"},
					"systemPrompt": true,
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

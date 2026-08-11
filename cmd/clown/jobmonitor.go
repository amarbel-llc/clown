package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/clown/internal/buildcfg"
	"code.linenisgreat.com/clown/internal/staging"
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

// troupeMonitorCommand returns the monitor command string for a persistent
// per-session troupe subcommand: `receive` (the troupe#3 minted-account receiver
// under transport=xmpp-native) or the legacy `agent` (RFC-0001, transport=xmpp).
// Both are the RECEIVE half of the XMPP backend: they stay joined and nudge this
// session's channel so the ringmaster monitor emits the wake.
//
// The absolute buildcfg.TroupePath is used for the same PATH-independence reason
// as the ringmaster monitor. The subcommand resolves its session key from
// CLOWN_SESSION_ID (jobwake.SessionKey()), which clown does NOT export ambiently
// (clown#136) and which takes no flag; it is threaded here as a SCOPED env prefix
// so the own-channel nudge targets THIS session's channel (matching the
// ringmaster monitor's --session key) without polluting the claude subtree env.
// The transport coordinates (TROUPE_TRANSPORT + TROUPE_XMPP_*, including the
// minted TROUPE_XMPP_USER/PASSWORD_FILE) and CLOWN_GROUP_ID are ambient
// (os.Setenv'd at launch) and inherited.
func troupeMonitorCommand(verb, key string) string {
	base := "troupe " + verb
	if buildcfg.TroupePath != "" {
		base = buildcfg.TroupePath + " " + verb
	}
	if key != "" {
		base = "env CLOWN_SESSION_ID=" + key + " " + base
	}
	return base
}

// mintSessionXMPPCredential mints this session's per-session XMPP account for the
// xmpp-native transport (troupe#3 Interface 1) and exports the resulting
// credential-by-reference so the `troupe receive` monitor and the `troupe mcp`
// child inherit it. It shells `troupe mint --session-key <key>` — idempotent on
// resume, prosodyctl-offline — and parses its {jid, password_file} JSON:
// TROUPE_XMPP_PASSWORD_FILE gets the file PATH (never the secret) and
// TROUPE_XMPP_USER the JID localpart. The vhost the mint provisions on comes from
// TROUPE_XMPP_DOMAIN, already exported from the [messaging] table before this
// runs.
//
// Best-effort, matching the mint's own contract and the presence registration at
// the call site: a failure logs and leaves the credential unset, so
// synthJobMonitorPluginDir skips the receiver (its TROUPE_XMPP_PASSWORD_FILE
// guard) and the session degrades to no-XMPP rather than breaking the launch or
// crash-looping a credential-less connect. A no-op when the troupe binary is
// absent (dev `go run`), where the receiver is skipped anyway.
func mintSessionXMPPCredential(key string) {
	if buildcfg.TroupePath == "" {
		return
	}
	cmd := exec.Command(buildcfg.TroupePath, "mint", "--session-key", key)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: troupe mint failed; xmpp-native messaging disabled this session: %v\n", err)
		return
	}
	var res struct {
		JID          string `json:"jid"`
		PasswordFile string `json:"password_file"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		fmt.Fprintf(os.Stderr, "clown: troupe mint: parsing output: %v\n", err)
		return
	}
	if res.PasswordFile != "" {
		_ = os.Setenv("TROUPE_XMPP_PASSWORD_FILE", res.PasswordFile)
	}
	if user, _, ok := strings.Cut(res.JID, "@"); ok && user != "" {
		_ = os.Setenv("TROUPE_XMPP_USER", user)
	}
}

// providerUsesPluginDirs reports whether the provider should receive clown's
// SYNTHESIZED plugin dirs (the job-watch monitor dir and the juggler-prompt
// dir). The requirement is simply that the provider CONSUMES them: codex
// ignores --plugin-dir, --naked bypasses clown's wrapping altogether, and
// juggler is a stub that ignores pluginDirs. Synthesizing dirs none of them
// reads would be pure waste.
//
// This is deliberately not a cleanup argument. The launch staging root owns
// these dirs now, so nothing here depends on a deferred removal firing — and
// the exec-replacing providers strand the root either way (codex writes its
// prompt file under it regardless; see BuildCodexArgs), so excluding them
// changes how much is left behind, not whether anything is.
//
// opencode and crush qualify since FDR 0016 phase 1 routed them through
// runWithPluginHost (cmd.Run, never syscall.Exec). What they take from the
// synthesized dir is its clown.json — the `ringmaster mcp` / `troupe mcp`
// stdioServers — which clown's own plugin host starts and hands them through
// their config's `mcp` block. They get the job platform's TOOLS (job_start,
// job_wait, chat_send, chat_read).
//
// They do NOT get the wake. The dir's .claude-plugin/plugin.json also declares
// `monitors`, and monitors are a Claude Code mechanism these two have no
// equivalent for; they simply never read that file, so the declaration is inert
// rather than harmful. The result is a session that can start and poll a
// background job (job_wait blocks) but is never woken when one finishes —
// degraded, not broken, and the deliberate phase-1 scope decision. Closing it
// is phase 3 and needs a server-mode invocation model, not a change here. The
// PreToolUse auto-allow hook is inert for the same reason, so clown's job tools
// will hit these providers' own permission prompts.
func providerUsesPluginDirs(provider string) bool {
	switch provider {
	case "claude", "clownbox", "opencode", "openrouter", "crush":
		return true
	default:
		return false
	}
}

// synthJobMonitorPluginDir writes a built-in plugin directory whose
// .claude-plugin/plugin.json declares the clown job-watch monitor, and returns
// its path. The caller appends the path to the --plugin-dir set passed to
// Claude; the launch's staging root owns the directory and removes it on
// close, so the caller must not. When CLOWN_DISABLE_JOB_WAKEUP=1 it returns
// ("", nil) so the monitor is not registered (RFC-0009 §8).
//
// root is required and a nil one panics, rather than being refused with an
// error the way CompilePluginDir and BuildClaudeArgs refuse theirs. The
// asymmetry is deliberate: those cross a package boundary and can be reached
// by callers this package does not control, whereas this has exactly one call
// site — in runWithFlags, where the root is always live — so a nil here is a
// programming error in this file, not input to validate.
func synthJobMonitorPluginDir(root *staging.Root, sessionKey string) (string, error) {
	if jobWakeupDisabled() {
		return "", nil
	}
	dir, err := root.Dir("clown-jobwake-plugin-*")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		return "", err
	}
	monitors := []jobMonitorEntry{{
		Name:        "ringmaster-monitor",
		Command:     monitorCommand(sessionKey),
		Description: "clown job-wakeup channel: wakes this session when a background job finishes",
	}}
	// When the session opts into a troupe XMPP transport (clownfile [messaging],
	// exported as TROUPE_TRANSPORT), also register the persistent per-session
	// receiver. It needs the troupe binary (nix builds); dev builds (empty
	// TroupePath) skip it, as they do the MCP servers below.
	switch os.Getenv("TROUPE_TRANSPORT") {
	case "xmpp-native":
		// troupe#3: the minted-account persistent receiver. `troupe receive`
		// joins the configured plain-JID rooms (TROUPE_XMPP_ROOMS) + this
		// session's own 1:1 inbox and emits EPHEMERAL wakes on the session's own
		// channel — no durable per-message record (clown#215 fixed structurally
		// on this path). Gated on the minted credential (TROUPE_XMPP_PASSWORD_FILE,
		// set by the mint-first step at launch): a mint miss leaves it unset, so
		// the receiver is not registered and the session degrades to no-XMPP
		// rather than crash-looping a credential-less connect.
		if buildcfg.TroupePath != "" && os.Getenv("TROUPE_XMPP_PASSWORD_FILE") != "" {
			monitors = append(monitors, jobMonitorEntry{
				Name:        "troupe-receive",
				Command:     troupeMonitorCommand("receive", sessionKey),
				Description: "troupe XMPP receiver (xmpp-native): rooms + 1:1 inbox, ephemeral wakes",
			})
		}
	case "xmpp":
		// Legacy RFC-0001 agent+journal path (kept until retired): joins the
		// mechanical 3-tier rooms and delivers inbound onto the local journal.
		if buildcfg.TroupePath != "" {
			monitors = append(monitors, jobMonitorEntry{
				Name:        "troupe-agent",
				Command:     troupeMonitorCommand("agent", sessionKey),
				Description: "troupe XMPP messaging receiver (legacy): delivers cross-host chat into this session",
			})
		}
	}
	manifest := jobMonitorPlugin{
		Name:     "clown-builtin-jobs",
		Version:  "1",
		Monitors: monitors,
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), b, 0o600); err != nil {
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
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "clown.json"), cb, 0o600); err != nil {
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
			return "", err
		}
		if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), hb, 0o600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

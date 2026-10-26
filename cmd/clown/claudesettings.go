package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// oneMillionWindow is the context window of a model selected with claude's
// `[1m]` suffix (or an `opus[1m]`/`sonnet[1m]`/`fable[1m]` alias).
const oneMillionWindow int64 = 1_000_000

// contextWindowForModel is the window a launch's --model value implies, or 0
// when it implies none. Only the `[1m]` suffix is knowable from the string
// (claude 2.1.306 selects the 1M window by it, and whether an account may use
// it is claude's call); a bare model id could be 200K or 1M, and a guess would
// wrongly warn on one of them, so it stays unknown and the guard stays quiet
// unless CLOWN_CONTEXT_WINDOW says otherwise.
func contextWindowForModel(model string) int64 {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]") {
		return oneMillionWindow
	}
	return 0
}

// afkTimeoutDisableMS is the CLAUDE_AFK_TIMEOUT_MS value clown injects to disable
// Claude Code's AskUserQuestion idle auto-continue (clown#163). Around Claude
// Code ~v2.1.196 an undocumented default shipped: when the user is idle ~60s at
// an AskUserQuestion prompt, the tool auto-returns "proceed using your best
// judgment" and the agent continues WITHOUT the user's answer — letting the
// agent bypass a genuine decision gate. The idle check is
// idle_ms >= (CLAUDE_AFK_TIMEOUT_MS ?? 60000), so 2147483647 (max int32,
// ~24.8 days) makes it effectively never fire. NEVER use 0: it fires instantly
// (auto-submits the moment the question appears — a confirmed footgun).
//
// CLAUDE_AFK_TIMEOUT_MS is community-reverse-engineered and NOT in Anthropic's
// published docs, so it may change or break without notice; it is a stopgap
// until a first-class control ships (clown#163).
const afkTimeoutDisableMS = "2147483647"

// profileDisallowedTools returns the extra --disallowed-tools entries a profile
// asked for through CLOWN_DISALLOWED_TOOLS (a comma-separated list in the
// profile's env table), layered over the global set. Blank entries are dropped.
func profileDisallowedTools() []string {
	var out []string
	for _, p := range strings.Split(os.Getenv("CLOWN_DISALLOWED_TOOLS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// claudeSafetySettingsJSON returns the JSON clown feeds to `claude --settings`
// for the claude family: clown's safety defaults. claude merges --settings as a
// settings scope that outranks the user, project, and local settings files (only
// a managed/policy source sits above it), so it adds only these keys without
// disturbing the user's other settings.
//
// Two kinds of default are injected:
//
//   - An "env" block. Each variable is injected ONLY when the user has not set
//     it themselves, and each is gated independently — an explicit user choice
//     on one does not suppress the other. When the user has overridden every
//     variable the env block is omitted.
//   - The top-level remoteControlAtStartup key, injected unconditionally
//     (it has no paired env var to gate on; see below).
//   - permissions.defaultMode = "dontAsk", only under CLOWN_UNATTENDED=1
//     (clown#323, unattendedEnv).
//
// Because remoteControlAtStartup is always present, this never returns "".
//
// Defaults:
//   - CLAUDE_AFK_TIMEOUT_MS = 2147483647 — disable AskUserQuestion idle
//     auto-continue so a decision gate cannot be bypassed (clown#163).
//   - CLAUDE_CODE_DISABLE_AUTO_MEMORY = 1 — disable Claude's autonomous auto
//     memory: the agent silently writing to and recalling
//     ~/.claude/projects/<proj>/memory/ (a MEMORY.md index + topic files).
//     CLAUDE.md / AGENTS.md are unaffected — only the agent-authored auto
//     memory is turned off (clown#164). Unlike the AFK var this is a DOCUMENTED
//     control (docs.claude.com/en/docs/claude-code/memory), read from any
//     settings scope including --settings; a user who wants auto memory can set
//     CLAUDE_CODE_DISABLE_AUTO_MEMORY=0 to opt back in.
//   - remoteControlAtStartup = false — disable Claude Code's Remote Control
//     auto-connect at session start (the "Enable Remote Control for all
//     sessions" /config option; docs.claude.com/en/docs/claude-code/remote-control).
//     This is a top-level settings key, not an env var, and has no paired
//     environment variable to gate on, so clown injects it unconditionally. A
//     user who wants Remote Control for one session can still pass claude's
//     built-in --remote-control flag, which re-enables it even when this key is
//     false. --settings outranks the user/project/local files, so this default
//     lands over a user's own remoteControlAtStartup; a managed source can still
//     override it.
//
// principal is the session's per-instance key. It is injected as
// CLOWN_SESSION_ID so hooks get the same identity the MCP servers do (clown#289):
// hook commands are spawned by claude from its own env, and clown never
// exports the key on its own env (clown#136), so the settings env block is the
// one channel that reaches them. Empty principal omits it. This puts the key in
// claude's whole subtree (Bash calls, subagents) — the accepted cost of hooks
// seeing it at all; plugin hooks.json commands are not clown's to prefix.
func claudeSafetySettingsJSON(principal, model string) string {
	settings := map[string]any{}

	env := map[string]string{}
	if principal != "" {
		env["CLOWN_SESSION_ID"] = principal
	}
	// The context-floor guard needs the launch's window; an operator's own
	// CLOWN_CONTEXT_WINDOW (env, profile env or clownfile env) is left alone.
	if os.Getenv("CLOWN_CONTEXT_WINDOW") == "" {
		if w := contextWindowForModel(model); w > 0 {
			env["CLOWN_CONTEXT_WINDOW"] = strconv.FormatInt(w, 10)
		}
	}
	if os.Getenv("CLAUDE_AFK_TIMEOUT_MS") == "" {
		env["CLAUDE_AFK_TIMEOUT_MS"] = afkTimeoutDisableMS
	}
	if os.Getenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY") == "" {
		env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"
	}
	// Context floor: a profile that sets CLOWN_DISABLE_CLAUDEAI_CONNECTORS=1
	// drops the claude.ai connectors' tool definitions. claude honors both the
	// setting and the env var (checked in the binary, 2.1.306); both are set so
	// either path suffices, and an explicit operator env var is left alone.
	if os.Getenv("CLOWN_DISABLE_CLAUDEAI_CONNECTORS") == "1" {
		settings["disableClaudeAiConnectors"] = true
		if os.Getenv("ENABLE_CLAUDEAI_MCP_SERVERS") == "" {
			env["ENABLE_CLAUDEAI_MCP_SERVERS"] = "false"
		}
	}
	if len(env) > 0 {
		settings["env"] = env
	}

	// The unattended posture (clown#323, FDR 0023): CLOWN_UNATTENDED=1 puts
	// claude in dontAsk. Any other value was refused by unattendedGate
	// before the args were built.
	if unattendedValue() == "1" {
		settings["permissions"] = map[string]any{"defaultMode": unattendedMode}
	}

	// Injected unconditionally — see the doc comment.
	settings["remoteControlAtStartup"] = false

	b, err := json.Marshal(settings)
	if err != nil {
		return ""
	}
	return string(b)
}

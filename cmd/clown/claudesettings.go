package main

import (
	"encoding/json"
	"os"
)

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
func claudeSafetySettingsJSON() string {
	settings := map[string]any{}

	env := map[string]string{}
	if os.Getenv("CLAUDE_AFK_TIMEOUT_MS") == "" {
		env["CLAUDE_AFK_TIMEOUT_MS"] = afkTimeoutDisableMS
	}
	if os.Getenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY") == "" {
		env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"
	}
	if len(env) > 0 {
		settings["env"] = env
	}

	// Injected unconditionally — see the doc comment.
	settings["remoteControlAtStartup"] = false

	b, err := json.Marshal(settings)
	if err != nil {
		return ""
	}
	return string(b)
}

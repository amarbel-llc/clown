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
// for the claude family: an "env" block carrying clown's safety defaults. claude
// merges --settings as a settings scope in the documented precedence cascade
// (user → project → local → policy → --settings), so it adds only these keys
// without disturbing the user's other settings.
//
// Each default is injected ONLY when the user has not set that variable
// themselves, and each is gated independently — an explicit user choice on one
// default does not suppress the other. Returns "" when the user has overridden
// every default (nothing left to inject).
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
func claudeSafetySettingsJSON() string {
	env := map[string]string{}
	if os.Getenv("CLAUDE_AFK_TIMEOUT_MS") == "" {
		env["CLAUDE_AFK_TIMEOUT_MS"] = afkTimeoutDisableMS
	}
	if os.Getenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY") == "" {
		env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] = "1"
	}
	if len(env) == 0 {
		return "" // user has overridden every safety default; inject nothing
	}
	b, err := json.Marshal(map[string]any{"env": env})
	if err != nil {
		return ""
	}
	return string(b)
}

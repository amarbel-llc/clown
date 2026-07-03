package main

import "os"

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
// for the claude family (clown#163): an "env" block forcing
// CLAUDE_AFK_TIMEOUT_MS so AskUserQuestion cannot be auto-continued past the
// user. clown ships no managed-settings (clown#133), so this rides the
// --settings CLI flag — a path claude actually reads — rather than a settings
// file. claude merges --settings as the highest-precedence CLI settings source,
// so it adds only this one env key without disturbing the user's
// permissions/hooks/other settings.
//
// It returns "" when CLAUDE_AFK_TIMEOUT_MS is already present in the
// environment, so an explicit user choice wins (including re-enabling
// auto-continue with a small value, or the 60000 default) — clown supplies only
// the safety default, it does not force the setting against a user who has
// spoken.
func claudeSafetySettingsJSON() string {
	if os.Getenv("CLAUDE_AFK_TIMEOUT_MS") != "" {
		return "" // user override present; respect it
	}
	return `{"env":{"CLAUDE_AFK_TIMEOUT_MS":"` + afkTimeoutDisableMS + `"}}`
}

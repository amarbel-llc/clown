package main

import (
	"os"

	"code.linenisgreat.com/clown/internal/clownfile"
)

// Hold claude's first turn until its MCP servers connect (clown#296).
//
// clown starts the plugin servers and waits for their /healthz before it
// launches claude, but claude then connects to them itself, non-blocking by
// default, so request 1 can go out with only the built-in tools. Both names
// below come from claude's binary strings, not its docs, and may change
// between claude versions. claude bounds the blocking connect with its own
// MCP_CONNECT_TIMEOUT_MS, so a failing server delays turn 1 but cannot hang it.
const (
	envMCPConnectionNonblocking = "MCP_CONNECTION_NONBLOCKING"
	envClaudeMCPStartupWaitMS   = "CLAUDE_CODE_MCP_STARTUP_WAIT_MS"
	defaultClaudeMCPStartupWait = "10000"
)

// claudeLaunchIsHeadless reports whether no human is watching claude's first
// turn: a detached spawn, a --print run, or a launch without a terminal.
func claudeLaunchIsHeadless(mode clownfile.AttachMode, forwarded []string, interactive bool) bool {
	return mode == clownfile.ModeSpawn ||
		claudeFlagPresent(forwarded, "--print", "-p") ||
		!interactive
}

// claudeMCPStartupEnv returns the variables clown adds to claude's env so its
// first turn waits for MCP servers. Only the claude provider gets them, and a
// variable already set in the inherited env (getenv non-empty, the same test
// as CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS) is left as the user set it.
func claudeMCPStartupEnv(provider string, headless bool, getenv func(string) string) map[string]string {
	if provider != "claude" {
		return nil
	}
	defaults := map[string]string{envMCPConnectionNonblocking: "false"}
	if headless {
		defaults[envClaudeMCPStartupWaitMS] = defaultClaudeMCPStartupWait
	}
	out := map[string]string{}
	for k, v := range defaults {
		if getenv(k) == "" {
			out[k] = v
		}
	}
	return out
}

// applyClaudeMCPStartupEnv sets claudeMCPStartupEnv's variables on clown's own
// env, which the claude child inherits. It runs before the [attach] re-exec,
// so the multiplexer is started with them too; whether the multiplexer passes
// them on to the inner clown is up to the multiplexer.
func applyClaudeMCPStartupEnv(provider string, headless bool) {
	for k, v := range claudeMCPStartupEnv(provider, headless, os.Getenv) {
		_ = os.Setenv(k, v)
	}
}

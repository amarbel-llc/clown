package main

import (
	"fmt"
	"os"
	"strings"

	"code.linenisgreat.com/ringmaster/jobwake"
)

// decideClaudeSession inspects the user's forwarded args plus the resolved base
// channel key and decides three things, returning them so the caller threads
// them explicitly (no process-env mutation — clown#136):
//
//  1. newForwarded — the args to pass claude, possibly with an injected
//     --session-id (so the resume id and the job-wakeup channel key are one,
//     RFC-0013 §2.1).
//  2. channelKey — the per-instance key the job-watch monitor (--session) and
//     the MCP producers (pluginhost BaseEnv) use.
//  3. hintID — the id to print as the post-exit `clown resume` hint, or "" for
//     no hint.
//
// Cases:
//
//   - --print/-p or --continue/-c: skip the hint (a --print one-shot is not
//     resumable and printing would pollute its stdout; --continue's id is
//     unknown without a transcript scan). The channel key stays baseKey.
//
//   - --session-id <id> or --resume <id> already present: adopt that id as both
//     the channel key and the hint; args unchanged.
//
//   - baseKey is UUID-shaped (minted, or from CLAUDE_SESSION_ID): inject
//     --session-id baseKey; channel key and hint are baseKey.
//
//   - baseKey is empty or a non-UUID operator override: mint a fresh uuid for
//     claude's --session-id and the hint. When baseKey is a non-empty operator
//     key, keep it as the channel (claude gets its own id); when empty, adopt
//     the minted id as the channel key too.
func decideClaudeSession(forwarded []string, baseKey string) (newForwarded []string, channelKey, hintID string) {
	if claudeFlagPresent(forwarded, "--print", "-p") || claudeFlagPresent(forwarded, "--continue", "-c") {
		return forwarded, baseKey, ""
	}
	if id := claudeFlagValue(forwarded, "--session-id"); id != "" {
		return forwarded, id, id
	}
	if id := claudeFlagValue(forwarded, "--resume", "-r"); id != "" {
		return forwarded, id, id
	}
	if isUUID(baseKey) {
		return append([]string{"--session-id", baseKey}, forwarded...), baseKey, baseKey
	}
	id := newUUIDv4()
	channelKey = baseKey
	if baseKey == "" {
		channelKey = id
	}
	return append([]string{"--session-id", id}, forwarded...), channelKey, id
}

// isUUID reports whether s is shaped like an RFC 4122 UUID (36 chars, hyphens at
// the canonical positions, hex elsewhere). It decides whether an existing
// CLOWN_SESSION_ID can double as the claude --session-id.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// claudeFlagPresent reports whether any of the supplied flag names
// appear in args. Matches both bare ("--print") and equals-form
// ("--print=true") spellings.
func claudeFlagPresent(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n {
				return true
			}
			if strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// claudeFlagValue returns the value of the first matching flag in args,
// supporting `--flag value` and `--flag=value` forms. Returns the empty
// string when none of the named flags are found.
func claudeFlagValue(args []string, names ...string) string {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, n+"=") {
				return strings.TrimPrefix(a, n+"=")
			}
		}
	}
	return ""
}

// printResumeHint writes the canonical `clown resume <uri>` line to
// stdout so the user can copy-paste it to reattach the session later.
// Single line, no prefix, no trailing context.
func printResumeHint(sessionID string) {
	fmt.Fprintf(os.Stdout, "clown resume clown://claude/%s\n", sessionID)
}

// newUUIDv4 returns a fresh UUIDv4 for use as a claude session id. It delegates
// to jobwake.NewUUID so the per-instance key generator (RFC-0013 §2.1) has a
// single implementation.
func newUUIDv4() string { return jobwake.NewUUID() }

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// prepareClaudeSessionID inspects the user's forwarded args and decides
// (1) whether a post-exit `clown resume clown://claude/<id>` hint should
// be printed, and (2) what id to print. Three cases:
//
//   - --print/-p or --continue/-c is set: skip the hint entirely.
//     --print is a one-shot; resuming it makes no sense and printing on
//     stdout would pollute pipelines. --continue resumes the most recent
//     session, but we cannot determine its id without a transcript scan.
//
//   - --resume <id> or --session-id <id> is already in args: keep args
//     unchanged, print the hint with the user-supplied id, and adopt that id
//     as the per-instance channel key (RFC-0013 §2.1).
//
//   - Neither: unify with the per-instance key ensureJobWakeupEnv already
//     minted (CLOWN_SESSION_ID, when UUID-shaped) as the claude --session-id,
//     else inject a freshly minted uuid. Either way the resume id and the
//     job-wakeup channel key are the same id.
//
// Returns the (possibly-modified) forwarded args and the id to print
// (empty string signals "no hint").
func prepareClaudeSessionID(forwarded []string) (newForwarded []string, sessionID string) {
	if claudeFlagPresent(forwarded, "--print", "-p") || claudeFlagPresent(forwarded, "--continue", "-c") {
		return forwarded, ""
	}
	if id := claudeFlagValue(forwarded, "--session-id"); id != "" {
		adoptInstanceKey(id)
		return forwarded, id
	}
	if id := claudeFlagValue(forwarded, "--resume", "-r"); id != "" {
		adoptInstanceKey(id)
		return forwarded, id
	}
	cs := os.Getenv("CLOWN_SESSION_ID")
	if isUUID(cs) {
		return append([]string{"--session-id", cs}, forwarded...), cs
	}
	id := newUUIDv4()
	if cs == "" {
		// No per-instance key yet (ensureJobWakeupEnv did not run, e.g. a direct
		// call) — adopt the minted id so the channel key and the claude
		// --session-id stay unified. A non-UUID cs is a deliberate operator
		// override: leave it as the channel and give claude its own id.
		adoptInstanceKey(id)
	}
	return append([]string{"--session-id", id}, forwarded...), id
}

// adoptInstanceKey sets the per-instance routing key (CLOWN_SESSION_ID) to the
// resolved claude session id, so a resumed or user-named session arms the
// channel matching its resume id (RFC-0013 §2.1). It runs inside
// withClaudeResumeHint before runWithPluginHost spawns any plugin server or the
// job-watch monitor, so every child inherits the unified key.
func adoptInstanceKey(id string) { _ = os.Setenv("CLOWN_SESSION_ID", id) }

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

// withClaudeResumeHint wraps a claude-style provider invocation with
// session-id injection and the post-exit hint. The run callback
// receives the (possibly-modified) forwarded args and returns the
// provider's exit code.
func withClaudeResumeHint(forwarded []string, run func(forwarded []string) int) int {
	newForwarded, id := prepareClaudeSessionID(forwarded)
	code := run(newForwarded)
	if id != "" {
		printResumeHint(id)
	}
	return code
}

// newUUIDv4 returns a fresh UUIDv4 for use as a claude session id. It delegates
// to jobwake.NewUUID so the per-instance key generator (RFC-0013 §2.1) has a
// single implementation.
func newUUIDv4() string { return jobwake.NewUUID() }

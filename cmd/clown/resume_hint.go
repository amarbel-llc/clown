package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
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
//     unchanged and print the hint with the user-supplied id.
//
//   - Neither: inject `--session-id <uuid>` so claude lands its
//     transcript at a known id, and print the hint with that uuid.
//
// Returns the (possibly-modified) forwarded args and the id to print
// (empty string signals "no hint").
func prepareClaudeSessionID(forwarded []string) (newForwarded []string, sessionID string) {
	if claudeFlagPresent(forwarded, "--print", "-p") || claudeFlagPresent(forwarded, "--continue", "-c") {
		return forwarded, ""
	}
	if id := claudeFlagValue(forwarded, "--session-id"); id != "" {
		return forwarded, id
	}
	if id := claudeFlagValue(forwarded, "--resume", "-r"); id != "" {
		return forwarded, id
	}
	id := newUUIDv4()
	return append([]string{"--session-id", id}, forwarded...), id
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

// newUUIDv4 generates a fresh UUIDv4 for use as a claude session id.
// Uses crypto/rand for randomness; on the extremely unlikely failure
// returns the zero UUID rather than aborting (the session is still
// valid and resumable, we just lose entropy).
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	// RFC 4122: set version (4) and variant (10x) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

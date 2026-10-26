package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/sessions"
)

// A confined session stays confined (clown#318). A confined launch records
// its --agent-scope spec in the session-names sidecar under every id it
// records (sessions.NameRecord.AgentScope). Every later launch that could
// resume one of those ids, whatever the entrypoint (`clown resume`, a
// direct --resume or --session-id, CLOWN_SESSION_ID, --naked), runs under
// that same spec or is refused: runWithFlags calls agentScopeForLaunch
// after decideClaudeSession, before anything is written or started.

// applyAgentScopeRecord settles flags.agentScopeSpec against the session's
// confinement record and returns a non-zero exit code when the launch is
// refused (the reason already on stderr).
func applyAgentScopeRecord(flags *parsedFlags) int {
	refuse := func(err error) int {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	if flags.agentScopeSpec != "" {
		canon, err := canonicalSpecPath(flags.agentScopeSpec)
		if err != nil {
			return refuse(err)
		}
		flags.agentScopeSpec = canon
	}
	spec, found, err := agentScopeForLaunch(launchScopeIDs(*flags), flags.agentScopeSpec, sessions.AgentScopeOf, func(path string) error {
		_, err := agentscope.LoadSpec(path)
		return err
	})
	if err != nil {
		return refuse(err)
	}
	flags.agentScopeSpec = spec
	switch {
	case found && (flags.naked || flags.tent || flags.provider != "claude"):
		return refuse(fmt.Errorf("this session ran confined under agent-scope spec %s, which --naked, --tent and a provider other than claude cannot honour; refusing to launch", spec))
	case spec != "" && flags.provider == "claude" && !flags.naked && confinedConversationID(*flags) == "":
		return refuse(errors.New("a confined launch must know the claude session it runs under, so it can record it: -c, the bare --resume picker and -p without --session-id or --resume <id> are refused under --agent-scope"))
	case flags.resumeCommand && !found && spec == "":
		fmt.Fprintf(os.Stderr, "clown: %s\n", unconfinedResumeNotice(claudeResumeValue(flags.forwarded)))
	}
	return 0
}

// launchScopeIDs are the ids whose confinement record a launch must honour:
// the ids it records under (lineageRecordIDs) and any conversation id the
// provider args name (--resume <id>, --session-id <id>), which a --naked or
// -p launch resumes without decideClaudeSession adopting it.
func launchScopeIDs(flags parsedFlags) []string {
	ids := lineageRecordIDs(flags.provider, flags.identity, flags.resumeHintID)
	for _, id := range []string{claudeResumeValue(flags.forwarded), claudeSessionIDArg(flags.forwarded)} {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// agentScopeForLaunch is the spec a launch with ids runs under, given the
// spec asked for (--agent-scope or CLOWN_AGENT_SCOPE, already canonical).
// found reports that some id carries a confinement record. Refused, never
// run unconfined: a record that cannot be read, ids recorded under
// different specs, a recorded path that is not absolute, a recorded spec
// that no longer loads, and a different spec asked for. A recorded
// session's spec cannot be changed by a later launch; a new spec means a
// new session. No record: the spec asked for, which may be empty.
func agentScopeForLaunch(ids []string, given string, recorded func(string) (string, bool, error), load func(string) error) (spec string, found bool, err error) {
	var from string
	for _, id := range ids {
		s, ok, err := recorded(id)
		if err != nil {
			return "", false, fmt.Errorf("reading whether session %s ran confined: %w; refusing to launch", id, err)
		}
		if !ok {
			continue
		}
		if found && s != spec {
			return "", false, fmt.Errorf("sessions %s and %s of this launch ran confined under different agent-scope specs (%s, %s); refusing to launch", from, id, spec, s)
		}
		spec, from, found = s, id, true
	}
	if !found {
		return given, false, nil
	}
	if !filepath.IsAbs(spec) {
		return "", false, fmt.Errorf("session %s ran confined under agent-scope spec %q, which is not absolute; refusing to launch", from, spec)
	}
	if given != "" && given != spec {
		return "", false, fmt.Errorf("session %s ran confined under agent-scope spec %s; refusing to run it under %s (a session's spec cannot be changed; start a new session for a new spec)", from, spec, given)
	}
	if err := load(spec); err != nil {
		return "", false, fmt.Errorf("session %s ran confined under agent-scope spec %s, which no longer loads (%v); refusing to run it unconfined", from, spec, err)
	}
	return spec, true, nil
}

// canonicalSpecPath is the form a spec path is recorded and compared in:
// absolute, cleaned, and with symlinks resolved when the file exists (a
// missing spec keeps its absolute path; loading it refuses later).
func canonicalSpecPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("agent-scope spec %q: %w", p, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	switch {
	case err == nil:
		return resolved, nil
	case errors.Is(err, fs.ErrNotExist):
		return abs, nil
	default:
		return "", fmt.Errorf("agent-scope spec %q: %w", p, err)
	}
}

// confinedConversationID is the claude conversation id a confined launch
// will run under, known before the launch, or "" when it is not: -c, the
// bare --resume picker, and -p with no --session-id or --resume <id> let
// claude choose. Such a confined launch could not record its id and would
// leave a resumable conversation with no confinement record, so it is
// refused.
func confinedConversationID(flags parsedFlags) string {
	for _, id := range []string{claudeSessionIDArg(flags.forwarded), claudeResumeValue(flags.forwarded), flags.resumeHintID} {
		if id != "" {
			return id
		}
	}
	return ""
}

// unconfinedResumeNotice is the stderr line for `clown resume` of a session
// with no confinement record and no spec asked for (clown#318): sessions
// confined before clown recorded confinement have none.
func unconfinedResumeNotice(id string) string {
	return "session " + id + " has no confinement record; resuming UNCONFINED (pass --agent-scope <spec> to confine it)"
}

// recordLaunchSessions writes build(id) for each id. A failed write is a
// warning on warn, except for a confined launch, where it refuses: without
// the record a later launch of the session would not know it ran confined.
func recordLaunchSessions(ids []string, confined bool, build func(string) sessions.NameRecord, record func(sessions.NameRecord) error, warn io.Writer) error {
	for _, id := range ids {
		if err := record(build(id)); err != nil {
			if confined {
				return fmt.Errorf("recording confined session %s in the session-names sidecar: %w; refusing", id, err)
			}
			fmt.Fprintf(warn, "clown: warning: recording session %s in the session-names sidecar: %v\n", id, err)
		}
	}
	return nil
}

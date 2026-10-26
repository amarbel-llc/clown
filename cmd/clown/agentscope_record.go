package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"code.linenisgreat.com/clown/internal/agentscope"
	"code.linenisgreat.com/clown/internal/sessions"
)

// A confined session stays confined (clown#318). A confined launch records
// its --agent-scope spec, and the digest of the spec's boundary, in the
// session-names sidecar under every id it records
// (sessions.NameRecord.AgentScope). A later launch that names one of those
// ids (`clown resume`, --resume/-r <id> in any spelling, --session-id,
// CLOWN_SESSION_ID, --naked, a restart) runs under that same spec, with the
// same boundary, or is refused: runWithFlags calls runConfinementGate after
// decideClaudeSession, before anything is written or started.
//
// Not covered, and documented as such (clown.1, FDR 0020):
//   - an unconfined -c/--continue or bare --resume picker launch: claude
//     picks the conversation, so it cannot be checked (clown#326);
//   - `clown agent-scope-run`, which takes its spec directly and records
//     nothing;
//   - sessions confined before clown recorded confinement (no record);
//   - a launch whose XDG_STATE_HOME differs from the one the record was
//     written under, since the sidecar lives there.

// confinementGate is the call runWithFlags makes once the session ids are
// final; a variable so a test can prove runWithFlags still calls it.
var confinementGate = runConfinementGate

// runConfinementGate settles flags.agentScopeSpec against the session's
// confinement record. A non-nil error refuses the launch; notices go to w.
func runConfinementGate(flags *parsedFlags, w io.Writer) error {
	resume, sessionIDs, unknown := claudeConversationRefs(flags.forwarded)
	if err := conversationRefsError(resume, sessionIDs, unknown, flags.identity.LaunchKey, sessions.AnyAgentScope); err != nil {
		return err
	}
	if flags.agentScopeSpec != "" {
		canon, err := canonicalSpecPath(flags.agentScopeSpec)
		if err != nil {
			return err
		}
		flags.agentScopeSpec = canon
	}
	spec, found, err := agentScopeForLaunch(launchScopeIDs(*flags), flags.agentScopeSpec, sessions.AgentScopeRecordOf, loadScopeSpec)
	if err != nil {
		return err
	}
	flags.agentScopeSpec = spec
	var digest string
	if spec != "" {
		// Recorded or given, the spec this launch runs under: loaded once more
		// for the digest to record (agentScopeForLaunch loaded a recorded one),
		// which the unit launch's own read must match.
		if digest, err = loadScopeSpec(spec); err != nil {
			return err
		}
	}
	flags.agentScopeDigest = digest
	confined := found || spec != ""
	switch {
	case found && (flags.naked || flags.tent || flags.provider != "claude"):
		return fmt.Errorf("this session ran confined under agent-scope spec %s, which --naked, --tent and a provider other than claude cannot honour; refusing to launch", spec)
	case confined && claudeFlagPresent(flags.forwarded, "--fork-session"):
		// The fork gets a new id claude mints, which a confined lineage cannot
		// follow: refused whether the source or this launch is confined.
		return errors.New("--fork-session of a confined session, or under --agent-scope, is refused: the fork's new session would carry no confinement record")
	case confined && claudeFlagPresent(flags.forwarded, "--from-pr"):
		return errors.New("--from-pr is refused for a confined session or under --agent-scope: claude picks the session, so it cannot be checked or recorded")
	case spec != "" && flags.provider == "claude" && !flags.naked && confinedConversationID(*flags) == "":
		return errors.New("a confined launch must know the claude session it runs under, so it can record it: -c, the bare --resume picker and -p without --session-id or --resume <id> are refused under --agent-scope")
	case !found && spec == "" && len(resume) > 0:
		// `clown resume` and a direct --resume <id> alike.
		fmt.Fprintf(w, "clown: %s\n", unconfinedResumeNotice(resume[0]))
	}
	if spec != "" && !flags.printLaunchPlan {
		// Recorded now, before anything starts, so a launch that dies early
		// still leaves the record; the later session-names records repeat it.
		ids := launchScopeIDs(*flags)
		if id := confinedConversationID(*flags); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
		build := func(id string) sessions.NameRecord {
			return sessions.NameRecord{SessionID: id, AgentScope: spec, AgentScopeDigest: digest}
		}
		if err := recordLaunchSessions(ids, true, build, sessions.RecordSession, w); err != nil {
			return err
		}
	}
	return nil
}

// claudeConversationRefs returns every conversation id the provider args
// name, in order: each --resume/-r value (--resume v, -r v, --resume=v,
// -r=v; a --resume followed by a flag or nothing is claude's picker and has
// no value, as in resumeArgRaw) and each --session-id value. resumeArgRaw
// and claudeFlagValue read only the first; claude's parser (commander)
// keeps the last of a repeated option (assumed, not verified against
// claude itself), so the confinement check reads them all and refuses when
// they disagree.
func claudeConversationRefs(args []string) (resume, sessionIDs []string, unknown bool) {
	for _, o := range resumeOccurrences(args) {
		switch {
		case o.ambiguous:
			unknown = true
		case o.value != "":
			resume = append(resume, normalizeSessionID(o.value))
		}
	}
	for i, a := range args {
		switch {
		case a == "--session-id":
			if i+1 < len(args) {
				sessionIDs = append(sessionIDs, normalizeSessionID(args[i+1]))
			}
		case strings.HasPrefix(a, "--session-id="):
			sessionIDs = append(sessionIDs, normalizeSessionID(strings.TrimPrefix(a, "--session-id=")))
		}
	}
	return resume, sessionIDs, unknown
}

// normalizeSessionID lower-cases a UUID, so a record and a lookup of the same
// session meet whatever case either was spelled in; anything else is kept.
func normalizeSessionID(id string) string {
	if isUUID(id) {
		return strings.ToLower(id)
	}
	return id
}

// loadLaunchSpec is the unit launch's read of the spec: it must still have
// the boundary the gate checked and recorded (digest, from loadScopeSpec),
// or the launch is refused. An empty digest (no gate ran: agent-scope-run)
// checks nothing.
func loadLaunchSpec(path, digest string) (agentscope.Spec, error) {
	s, err := agentscope.LoadSpec(path)
	if err != nil {
		return agentscope.Spec{}, err
	}
	if digest != "" {
		if got := boundaryDigest(s); got != digest {
			return agentscope.Spec{}, fmt.Errorf("agent scope spec %s changed between the confinement check and the unit launch (boundary sha256 %s, now %s); refusing", path, digest, got)
		}
	}
	return s, nil
}

// boundaryDigest is the sha256 of what a spec lets the unit reach, the part
// a session must keep across its launches (clown#318): the worktree, the
// operator home, binds_rw and binds_ro, deny_paths except wayland sockets,
// egress_allow (unset, meaning clown's default list, differs from empty),
// and the sockets' purposes. Lists are cleaned, sorted and de-duplicated.
// Not covered, because spinclass rewrites them legitimately on every spawn:
// env (identity, description), socket paths, unit_name_hint, result_path,
// and the wayland-* deny entries (which follow the display server). The
// spec is not agent-writable in production, so this is defence in depth.
func boundaryDigest(s agentscope.Spec) string {
	norm := func(ps []string, keep func(string) bool) []string {
		var out []string
		for _, p := range ps {
			if keep == nil || keep(p) {
				out = append(out, filepath.Clean(p))
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	purposes := make([]string, 0, len(s.Sockets))
	for _, sock := range s.Sockets {
		purposes = append(purposes, sock.Purpose)
	}
	slices.Sort(purposes)
	b := struct {
		Worktree, OperatorHome      string
		BindsRW, BindsRO, DenyPaths []string
		EgressDefault               bool
		EgressAllow                 []string
		SocketPurposes              []string
	}{
		Worktree: filepath.Clean(s.Worktree), OperatorHome: filepath.Clean(s.OperatorHome),
		BindsRW: norm(s.BindsRW, nil), BindsRO: norm(s.BindsRO, nil),
		DenyPaths:      norm(s.DenyPaths, func(p string) bool { return !strings.HasPrefix(filepath.Base(p), "wayland-") }),
		EgressDefault:  s.EgressAllow == nil,
		EgressAllow:    slices.Compact(slices.Sorted(slices.Values(s.EgressAllow))),
		SocketPurposes: purposes,
	}
	raw, _ := json.Marshal(b) // a struct of strings and bools always marshals
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// distinct returns vs without repeats, in first-seen order.
func distinct(vs []string) []string {
	var out []string
	for _, v := range vs {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// conversationRefsError refuses provider args the confinement check cannot
// read unambiguously: repeated --resume or --session-id values that
// disagree, and, while any session is recorded as confined, a --resume
// search term (not a UUID) or a short-option cluster carrying -r (unknown),
// since neither can be tied to one record. A term equal to launchKey, the
// session key this launch was given (a restart's --resume <key>), is that
// session's own id and is looked up as such, so it is not refused. The
// refusal lasts as long as any confinement record does: the sidecar is
// append-only and never pruned.
func conversationRefsError(resume, sessionIDs []string, unknown bool, launchKey string, anyConfined func() (bool, error)) error {
	if d := distinct(resume); len(d) > 1 {
		return fmt.Errorf("--resume names more than one session (%s); refusing, since only one can be checked against its confinement record", strings.Join(d, ", "))
	}
	if d := distinct(sessionIDs); len(d) > 1 {
		return fmt.Errorf("--session-id names more than one session (%s); refusing, since only one can be checked against its confinement record", strings.Join(d, ", "))
	}
	var what string
	switch {
	case unknown:
		what = "a short-option cluster carrying -r"
	case len(resume) > 0 && !isUUID(resume[0]) && (launchKey == "" || resume[0] != launchKey):
		what = fmt.Sprintf("--resume %q, a search term,", resume[0])
	default:
		return nil
	}
	confined, err := anyConfined()
	if err != nil {
		return fmt.Errorf("reading the confinement records: %w; refusing %s", err, what)
	}
	if confined {
		return fmt.Errorf("%s cannot be checked against the confinement records some sessions carry; resume by the session's UUID (--resume <uuid>) or with `clown resume`", what)
	}
	return nil
}

// launchScopeIDs are the ids whose confinement record a launch must honour:
// the ids it records under (lineageRecordIDs) and any conversation id the
// provider args name (--resume <id>, --session-id <id>), which a --naked or
// -p launch resumes without decideClaudeSession adopting it.
func launchScopeIDs(flags parsedFlags) []string {
	var ids []string
	resume, sessionIDs, _ := claudeConversationRefs(flags.forwarded)
	for _, id := range slices.Concat(lineageRecordIDs(flags.provider, flags.identity, flags.resumeHintID), resume, sessionIDs) {
		id = normalizeSessionID(id)
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
// that no longer loads, a spec whose bytes changed since the last confined
// launch recorded their digest, and a different spec asked for. A recorded
// session's spec cannot be changed by a later launch, in path or in
// content; a new spec means a new session. No record: the spec asked for,
// which may be empty. load returns the digest of the spec bytes it
// validated.
func agentScopeForLaunch(ids []string, given string, recorded func(string) (sessions.AgentScopeRecord, bool, error), load func(string) (string, error)) (spec string, found bool, err error) {
	var from string
	var rec sessions.AgentScopeRecord
	for _, id := range ids {
		r, ok, err := recorded(id)
		if err != nil {
			return "", false, fmt.Errorf("reading whether session %s ran confined: %w; refusing to launch", id, err)
		}
		if !ok {
			continue
		}
		if found && r != rec {
			return "", false, fmt.Errorf("sessions %s and %s of this launch ran confined under different agent-scope specs (%s, %s); refusing to launch", from, id, rec.Spec, r.Spec)
		}
		rec, from, found = r, id, true
	}
	if !found {
		return given, false, nil
	}
	spec = rec.Spec
	if !filepath.IsAbs(spec) {
		return "", false, fmt.Errorf("session %s ran confined under agent-scope spec %q, which is not absolute; refusing to launch", from, spec)
	}
	if given != "" && given != spec {
		return "", false, fmt.Errorf("session %s ran confined under agent-scope spec %s; refusing to run it under %s (a session's spec cannot be changed; start a new session for a new spec)", from, spec, given)
	}
	digest, err := load(spec)
	if err != nil {
		return "", false, fmt.Errorf("session %s ran confined under agent-scope spec %s, which no longer loads (%v); refusing to run it unconfined", from, spec, err)
	}
	if rec.Digest != "" && digest != rec.Digest {
		return "", false, fmt.Errorf("session %s ran confined under agent-scope spec %s, which has changed since its last confined launch (sha256 %s, now %s); refusing (a changed spec means a new session)", from, spec, rec.Digest, digest)
	}
	return spec, true, nil
}

// loadScopeSpec loads a session launch's spec and returns the sha256 of the
// bytes it validated. A spec file the unit itself could rewrite, inside the
// spec's own worktree or a binds_rw entry, is refused: its next launch would
// honour whatever the agent wrote (clown#318). `clown agent-scope-run`
// does not come through here.
func loadScopeSpec(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("agent scope spec: %w", err)
	}
	s, err := agentscope.ParseSpec(path, raw)
	if err != nil {
		return "", err
	}
	resolved := agentscope.ResolvePath(path)
	for _, rw := range append([]string{s.Worktree}, s.BindsRW...) {
		if r := agentscope.ResolvePath(rw); resolved == r || strings.HasPrefix(resolved, r+"/") {
			return "", fmt.Errorf("agent scope spec %s is writable from inside its unit (under %s); refusing", path, rw)
		}
	}
	return boundaryDigest(s), nil
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
			return normalizeSessionID(id)
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

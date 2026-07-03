package jobwake

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var jobIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ErrInvalidJobID is wrapped by every validateJobID failure so callers (notably
// the CLI) can distinguish a usage error — a malformed job id, which exits 2 —
// from a runtime error such as a missing journal, which exits 1.
var ErrInvalidJobID = errors.New("invalid job id")

// validateJobID enforces the RFC-0009 §4 job-id grammar before an id is used to
// compose a filesystem path, and additionally rejects "." and ".." which the
// grammar admits. The grammar excludes "/", so a traversal id like "../foo" is
// already a grammar failure; the explicit "."/".." reject is belt-and-suspenders
// for the forms that survive suffix stripping. Every path-composing entry point
// (appendRecord for the write side, ReadJob for the read side) calls this so the
// §4 grammar is enforced in code, not merely documented (clown#123). Failures
// wrap ErrInvalidJobID so callers can branch on errors.Is.
func validateJobID(id string) error {
	if !jobIDRe.MatchString(id) {
		return fmt.Errorf("%w %q: must match %s", ErrInvalidJobID, id, jobIDRe.String())
	}
	if id == "." || id == ".." {
		return fmt.Errorf("%w %q: must not be %q or %q", ErrInvalidJobID, id, ".", "..")
	}
	return nil
}

// StartOpts configures a new job. Target overrides the resolved SessionKey;
// Source identifies the emitting plugin; Label seeds the generated job id.
type StartOpts struct {
	Target string // session key; empty => SessionKey()
	Label  string
	Source string
}

func newJobID(label string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	suf := hex.EncodeToString(b)
	label = sanitizeLabel(label)
	if label == "" {
		return suf
	}
	return label + "-" + suf
}

func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
	return strings.Trim(s, "-")
}

func nowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// defaultSource resolves a producer's source label: the explicit value when
// non-empty, else CLOWN_JOB_SOURCE, else "clown".
func defaultSource(source string) string {
	if source != "" {
		return source
	}
	if v := os.Getenv("CLOWN_JOB_SOURCE"); v != "" {
		return v
	}
	return "clown"
}

// resolveSession picks the session key a producer operation writes to: the
// explicit target when non-empty (so a cross-session producer started with
// `ringmaster start --target <key>` keeps writing to that channel through
// progress/done), else the resolved SessionKey() of the current session
// (RFC-0009 §2, §8).
func resolveSession(target string) string {
	if target != "" {
		return target
	}
	return SessionKey()
}

// Start allocates a job id, creates the channel journal directory (mode 0700),
// and appends the seq-0 `started` record (RFC-0009 §8). It returns the job id.
func Start(o StartOpts) (string, error) {
	session := resolveSession(o.Target)
	source := defaultSource(o.Source)
	cid := ChannelID(session)
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		return "", err
	}
	id := newJobID(o.Label)
	if err := validateJobID(id); err != nil {
		return "", fmt.Errorf("generated job id %q invalid: %w", id, err)
	}
	rec := Record{V: SchemaVersion, Job: id, Session: session, Source: source,
		Type: TypeStarted, TS: nowTS()}
	if err := appendRecord(cid, rec, false); err != nil {
		return "", err
	}
	return id, nil
}

// appendRecord appends a record to its job journal. It reads the existing
// records to derive the next seq (single writer => existing count), reject an
// append after a terminal record, and carry the started record's source
// forward when the caller leaves it empty. A terminal append fsyncs the file so
// the journal is durable before any nudge (RFC-0009 §7).
//
// appendRecord is NOT safe for concurrent writers to the same job: it derives
// the next seq from the current record count, so two racing writers would
// assign the same seq. RFC-0009 §7 requires a job to have a single writer (the
// clown-job CLI is one process per job operation); this invariant is the
// caller's responsibility, not enforced here.
func appendRecord(cid string, partial Record, fsync bool) error {
	if err := validateJobID(partial.Job); err != nil {
		return err
	}
	existing, err := ReadJob(cid, partial.Job)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, r := range existing {
		if IsTerminal(r.Type) {
			return fmt.Errorf("job %q already terminal (%s)", partial.Job, r.Type)
		}
	}
	partial.Seq = len(existing) // 0,1,2,... since the single writer appends in order
	if partial.Source == "" && len(existing) > 0 {
		partial.Source = existing[0].Source
	}
	line, err := json.Marshal(partial)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(JournalFile(cid, partial.Job), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	if fsync {
		return f.Sync()
	}
	return nil
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}

// SplitMessage parses a git-commit-style chat message into a one-line subject
// (the wake) and the remaining body (the spool). The convention mirrors a git
// commit: a concise summary line, a blank line, then the detail body. A
// single-line message is a legitimate short chat with no body. A body that
// follows the summary WITHOUT an intervening blank line is rejected — the
// caller surfaces the error rather than silently folding the detail into the
// one-line wake. The returned subject/body are passed straight to
// SendChat, so the durable storage model (subject in the journal record, body
// in the output spool) is unchanged.
func SplitMessage(message string) (subject, body string, err error) {
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	subject = strings.TrimSpace(lines[0])
	// Single line, or nothing but blank lines after the summary: subject-only.
	if len(lines) == 1 || strings.TrimSpace(strings.Join(lines[1:], "\n")) == "" {
		return subject, "", nil
	}
	// A body is present: the line immediately after the summary must be blank
	// (the git-commit separator). Whitespace-only counts as blank.
	if strings.TrimSpace(lines[1]) != "" {
		return "", "", fmt.Errorf("git-commit format: separate the subject and body with a blank line")
	}
	body = strings.TrimSpace(strings.Join(lines[2:], "\n"))
	return subject, body, nil
}

// Progress appends a non-waking progress record. It is journal-only and never
// wakes the agent, so it sends no nudge (RFC-0009 §5, §8). target selects the
// channel: empty resolves the current session, else the cross-session target
// the job was started with (mirrors Start's StartOpts.Target).
func Progress(target, jobID, message string) error {
	session := resolveSession(target)
	cid := ChannelID(session)
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: TypeProgress,
		TS: nowTS(), Message: oneLine(message)}
	if err := appendRecord(cid, rec, false); err != nil {
		return err
	}
	// No nudge: progress is journal-only (RFC-0009 §5), never wakes; the monitor's
	// periodic rescan picks it up for pull (clown job-read) without a nudge.
	return nil
}

// Message emits a standalone waking-event job (RFC-0009 §4 carve-out): one
// self-contained single-record job of the non-terminal waking type `message`,
// with no started and no terminal record. target selects the channel: empty
// resolves the current session, an explicit key targets that session, and
// BroadcastKey ("*") targets the broadcast channel. from is the OPTIONAL
// sender session key carried in the record's `from` field. The record is
// fsynced before any nudge (waking => durable-first, RFC-0009 §7); broadcast
// records get NO nudge — the monitors' periodic rescan is the delivery path
// (RFC-0009 §6). It returns the generated job id (`msg-<8hex>`).
func Message(target, source, from, body, resultRef string, resources ...Resource) (string, error) {
	session := resolveSession(target)
	source = defaultSource(source)
	cid := ChannelID(session)
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		return "", err
	}
	id := newJobID("msg")
	rec := Record{V: SchemaVersion, Job: id, Session: session, Source: source,
		From: from, Type: TypeMessage, TS: nowTS(), Message: oneLine(body),
		ResultRef: resultRef, Resources: resources}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return "", err
	}
	if session != BroadcastKey {
		sendNudge(cid, id, TypeMessage)
	}
	return id, nil
}

// SendChat emits a chat message (RFC-0013 §3): a waking `chat` record carrying
// the one-line SUBJECT (the wake notification) plus the full multi-line BODY
// written to the message's output spool (the body store, RFC-0010), so a long
// body never re-triggers the subject-line truncation guard (#103). Unlike a
// plain `message` wake, a `chat` record is NOT reaped on delivery (TypeChat is
// not TypeMessage, which the own-channel reap targets) — the body must survive
// for chat-read, so it rests until the age-based GC. target selects the channel
// like Message (per-instance key / SPINCLASS group / BroadcastKey). from is the
// OPTIONAL sender session key. The body is written before the record + nudge, so
// any reader that discovers the record always finds its body. Returns the
// generated job id (`chat-<8hex>`).
func SendChat(target, from, source, subject, body string, resources ...Resource) (string, error) {
	session := resolveSession(target)
	source = defaultSource(source)
	cid := ChannelID(session)
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		return "", err
	}
	id := newJobID("chat")
	if body != "" {
		if err := os.WriteFile(SpoolFile(cid, id), []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	rec := Record{V: SchemaVersion, Job: id, Session: session, Source: source,
		From: from, Type: TypeChat, TS: nowTS(), Message: oneLine(subject),
		Resources: resources}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return "", err
	}
	if session != BroadcastKey {
		sendNudge(cid, id, TypeChat)
	}
	return id, nil
}

// Done appends the single terminal record (fsynced) and then sends the nudge,
// guaranteeing the journal is durable before the socket (RFC-0009 §7). It
// rejects a non-terminal state and a second terminal append (RFC-0009 §5, §8).
// target selects the channel: empty resolves the current session, else the
// cross-session target the job was started with (mirrors Start's
// StartOpts.Target), so a cross-session producer's done wakes the right
// session.
func Done(target, jobID, state, message, resultRef string, resources ...Resource) error {
	if !IsTerminal(state) {
		return fmt.Errorf("invalid terminal state %q", state)
	}
	session := resolveSession(target)
	cid := ChannelID(session)
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: state,
		TS: nowTS(), Message: oneLine(message), ResultRef: resultRef, Resources: resources}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return err
	}
	sendNudge(cid, jobID, state)
	return nil
}

// DoneChannel is Done addressed by an explicit channel id instead of a session
// key/target — the raw-channel cancellation path behind `ringmaster cancel
// --channel`. It recovers the originating session key from the job's existing
// records so the terminal record stays well-formed, appends the terminal record
// (fsynced), then nudges the channel socket: the owning session's monitor binds
// SocketPath(cid) — the same channel — so the wake still lands without ever
// needing the pre-image session key. An invalid channel id wraps
// ErrInvalidChannelID.
func DoneChannel(cid, jobID, state, message, resultRef string) error {
	if !IsTerminal(state) {
		return fmt.Errorf("invalid terminal state %q", state)
	}
	if err := ValidateChannelID(cid); err != nil {
		return err
	}
	session := ""
	if recs, err := ReadJob(cid, jobID); err == nil && len(recs) > 0 {
		session = recs[0].Session // keep the terminal record's session consistent
	}
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: state,
		TS: nowTS(), Message: oneLine(message), ResultRef: resultRef}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return err
	}
	sendNudge(cid, jobID, state)
	return nil
}

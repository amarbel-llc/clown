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

// resolveSession picks the session key a producer operation writes to: the
// explicit target when non-empty (so a cross-session producer started with
// `clown job start --target <key>` keeps writing to that channel through
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
	source := o.Source
	if source == "" {
		if v := os.Getenv("CLOWN_JOB_SOURCE"); v != "" {
			source = v
		} else {
			source = "clown"
		}
	}
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
func Message(target, source, from, body, resultRef string) (string, error) {
	session := resolveSession(target)
	if source == "" {
		if v := os.Getenv("CLOWN_JOB_SOURCE"); v != "" {
			source = v
		} else {
			source = "clown"
		}
	}
	cid := ChannelID(session)
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		return "", err
	}
	id := newJobID("msg")
	rec := Record{V: SchemaVersion, Job: id, Session: session, Source: source,
		From: from, Type: TypeMessage, TS: nowTS(), Message: oneLine(body),
		ResultRef: resultRef}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return "", err
	}
	if session != BroadcastKey {
		sendNudge(cid, id, TypeMessage)
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
func Done(target, jobID, state, message, resultRef string) error {
	if !IsTerminal(state) {
		return fmt.Errorf("invalid terminal state %q", state)
	}
	session := resolveSession(target)
	cid := ChannelID(session)
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: state,
		TS: nowTS(), Message: oneLine(message), ResultRef: resultRef}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return err
	}
	sendNudge(cid, jobID, state)
	return nil
}

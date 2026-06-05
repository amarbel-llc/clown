package jobwake

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var jobIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

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

// Start allocates a job id, creates the channel journal directory (mode 0700),
// and appends the seq-0 `started` record (RFC-0009 §8). It returns the job id.
func Start(o StartOpts) (string, error) {
	session := o.Target
	if session == "" {
		session = SessionKey()
	}
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
	if !jobIDRe.MatchString(id) {
		return "", fmt.Errorf("generated job id %q is invalid", id)
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
func appendRecord(cid string, partial Record, fsync bool) error {
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

// Progress appends a non-waking progress record and sends a best-effort nudge.
// It is journal-only and never wakes the agent (RFC-0009 §5, §8).
func Progress(jobID, message string) error {
	session := SessionKey()
	cid := ChannelID(session)
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: TypeProgress,
		TS: nowTS(), Message: oneLine(message)}
	if err := appendRecord(cid, rec, false); err != nil {
		return err
	}
	sendNudge(cid, jobID, TypeProgress)
	return nil
}

// Done appends the single terminal record (fsynced) and then sends the nudge,
// guaranteeing the journal is durable before the socket (RFC-0009 §7). It
// rejects a non-terminal state and a second terminal append (RFC-0009 §5, §8).
func Done(jobID, state, message, resultRef string) error {
	if !IsTerminal(state) {
		return fmt.Errorf("invalid terminal state %q", state)
	}
	session := SessionKey()
	cid := ChannelID(session)
	rec := Record{V: SchemaVersion, Job: jobID, Session: session, Type: state,
		TS: nowTS(), Message: oneLine(message), ResultRef: resultRef}
	if err := appendRecord(cid, rec, true); err != nil { // fsync before nudge
		return err
	}
	sendNudge(cid, jobID, state)
	return nil
}

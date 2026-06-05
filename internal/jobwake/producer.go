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

// appendRecord appends a record to its job journal, deriving seq from the
// existing record count (single writer). Task 4 extends this with terminal
// detection, source carry-forward, and fsync; for Start the minimal form is a
// direct count + append.
func appendRecord(cid string, partial Record, fsync bool) error {
	f, err := os.OpenFile(JournalFile(cid, partial.Job), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(partial)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	if fsync {
		return f.Sync()
	}
	return nil
}

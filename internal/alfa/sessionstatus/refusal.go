package sessionstatus

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Restart-refusal kinds (RestartRefusal.Kind). A reader that does not know a
// kind treats it as "a consumed restart request was dropped".
const (
	// RefusalProfileGate: the dry-run of the relaunch's profile gate refused
	// before anything was stopped; the session keeps running.
	RefusalProfileGate = "profile-gate"
	// RefusalResume: the relaunch's own resume gate refused after the stop;
	// the frontend exits.
	RefusalResume = "resume"
	// RefusalCountdown: the countdown before the stop was cancelled.
	RefusalCountdown = "countdown"
	// RefusalTurnStarted: a turn started during the countdown.
	RefusalTurnStarted = "turn-started"
)

// MaxRefusalText bounds RestartRefusal.Text.
const MaxRefusalText = 300

// RestartRefusal records a restart that spinclass's preflight consumed but the
// frontend did not carry out, so the requester must ask again. Written only by
// the frontend, and dropped by construction when the next launch of the
// principal opens a new file. Digests are empty where the frontend did not
// have them; empty is unknown.
type RestartRefusal struct {
	// At is when it was dropped (timeFormat); spinclass dedups on it.
	At            string `json:"at"`
	Kind          string `json:"kind"`
	Profile       string `json:"profile,omitempty"`
	PinnedDigest  string `json:"pinned_digest,omitempty"`
	CurrentDigest string `json:"current_digest,omitempty"`
	// Text is one printable line, at most MaxRefusalText bytes.
	Text string `json:"text"`
}

// SetRestartRefusal records r in the status file at once (it is not a turn
// hook, so it is not coalesced). An empty At is stamped now; Text is reduced
// to one bounded printable line.
func (w *Writer) SetRestartRefusal(r RestartRefusal) {
	r = cleanRefusal(r)
	w.update(func(st *Status) { st.RestartRefusal = &r }, false)
}

// WriteRestartRefusal writes principal's status file once, for a frontend that
// refused its own relaunch and is about to exit (no Writer exists yet).
func WriteRestartRefusal(principal, spinclassKey string, r RestartRefusal) error {
	w, err := Open(principal, spinclassKey, nil)
	if err != nil {
		return err
	}
	r = cleanRefusal(r)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.st.RestartRefusal = &r
	w.st.WrittenAt = now()
	return w.write()
}

func cleanRefusal(r RestartRefusal) RestartRefusal {
	if r.At == "" {
		r.At = now()
	}
	r.Text = oneBoundedLine(r.Text, MaxRefusalText)
	return r
}

// oneBoundedLine turns s into one line of printable text of at most max
// bytes, cut on a character boundary.
func oneBoundedLine(s string, limit int) string {
	s = strings.Map(func(c rune) rune {
		switch {
		case c == '\n' || c == '\r' || c == '\t':
			return ' '
		case !unicode.IsPrint(c):
			return -1
		}
		return c
	}, s)
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

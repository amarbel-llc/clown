package jobwake

import (
	"bytes"
	"io"
	"os"
	"strings"
	"time"
)

// spoolTailWindow bounds how much of the spool's tail the status probe reads: it
// always reads at most this many bytes from EOF, so a probe never scales with
// spool size (RFC-0010 §3). A partial line at the window's leading edge is
// dropped.
const spoolTailWindow = 64 * 1024

// SpoolPath validates the job id, ensures the channel journal directory exists,
// and returns the absolute output-spool path for the job (RFC-0010 §2). It does
// NOT create the spool file — that is the producer's append. target selects the
// channel exactly as `clown job start --target` does (empty => current session).
func SpoolPath(target, jobID string) (string, error) {
	if err := validateJobID(jobID); err != nil {
		return "", err
	}
	cid := ChannelID(resolveSession(target))
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		return "", err
	}
	return SpoolFile(cid, jobID), nil
}

// Status is the journal+spool-derived view of one job (RFC-0010 §3). The JSON
// field names are the contract the consumer (moxy async-result) mirrors
// verbatim; absent optionals are omitted.
type Status struct {
	State        string   `json:"state"`
	Source       string   `json:"source"`
	Started      string   `json:"started"`
	Ended        string   `json:"ended,omitempty"`
	ElapsedSec   int64    `json:"elapsed_sec"`
	LastActivity string   `json:"last_activity,omitempty"`
	SpoolBytes   int64    `json:"spool_bytes"`
	Progress     string   `json:"progress,omitempty"`
	Tail         []string `json:"tail,omitempty"`
}

// StatusOf derives a job's status from its journal and spool alone (RFC-0010 §3).
// It is a read-only probe, available regardless of CLOWN_DISABLE_JOB_WAKEUP. now
// is injected (mirroring gc.sweep) so a running job's elapsed/last_activity are
// deterministic in tests; callers pass time.Now().UTC(). A missing or empty
// journal returns an error satisfying os.IsNotExist; an invalid job id returns an
// error wrapping ErrInvalidJobID (both surfaced via ReadJob). It never infers
// producer liveness: a job whose producer died without a terminal record reports
// `running` with a stale last_activity (the RFC-0009 §10 gap is unchanged).
func StatusOf(target, jobID string, tailN int, now time.Time) (Status, error) {
	cid := ChannelID(resolveSession(target))
	recs, err := ReadJob(cid, jobID)
	if err != nil {
		return Status{}, err
	}
	if len(recs) == 0 {
		return Status{}, os.ErrNotExist // present-but-empty journal: nothing to report
	}

	st := Status{State: "running", Source: recs[0].Source, Started: recs[0].TS}
	for _, r := range recs {
		if r.Type == TypeProgress {
			st.Progress = r.Message
		}
		if IsTerminal(r.Type) {
			st.State = r.Type
			st.Ended = r.TS
		}
	}

	started := parseTS(recs[0].TS)
	end := now
	if st.Ended != "" {
		end = parseTS(st.Ended)
	}
	st.ElapsedSec = int64(end.Sub(started).Seconds())

	// last_activity = max(spool mtime, newest journal record ts): a progress
	// record written after the last output must not understate liveness, and a
	// spool appended after the last record must not either (RFC-0010 §3).
	lastAct := parseTS(recs[len(recs)-1].TS)
	if info, err := os.Stat(SpoolFile(cid, jobID)); err == nil {
		st.SpoolBytes = info.Size()
		if mt := info.ModTime(); mt.After(lastAct) {
			lastAct = mt
		}
		st.Tail = tailLines(SpoolFile(cid, jobID), tailN)
	}
	if !lastAct.IsZero() {
		st.LastActivity = lastAct.UTC().Format(time.RFC3339Nano)
	}
	return st, nil
}

// parseTS parses an RFC3339Nano journal timestamp, returning the zero time on a
// malformed value (the record is still surfaced; only the derived duration is
// affected).
func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// tailLines returns the last n lines of the spool, read from a bounded trailing
// window so the probe never scales with spool size (RFC-0010 §3). A partial line
// at the leading edge of the window is dropped. Returns nil on any read error or
// an empty spool.
func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if info.Size() > spoolTailWindow {
		start = info.Size() - spoolTailWindow
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:] // drop the partial first line inside the window
		}
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

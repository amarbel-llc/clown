package jobwake

import (
	"bytes"
	"fmt"
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

// ResolveSpool validates the job id and returns the absolute output-spool path
// for the job in the channel resolved from target, creating nothing (no file,
// no directory). It is the read-only path resolver the operator tail surface
// uses; SpoolPath is the producer-facing variant that also ensures the channel
// dir exists. An invalid job id wraps ErrInvalidJobID.
func ResolveSpool(target, jobID string) (string, error) {
	if err := validateJobID(jobID); err != nil {
		return "", err
	}
	return SpoolFile(ChannelID(resolveSession(target)), jobID), nil
}

// ResolveSpoolChannel is ResolveSpool addressed by an explicit channel id rather
// than a session key/target — the raw-channel path behind `ringmaster tail
// --channel`. It validates both the channel id and the job id, creates nothing,
// and returns the spool path. An invalid channel id wraps ErrInvalidChannelID;
// an invalid job id wraps ErrInvalidJobID.
func ResolveSpoolChannel(cid, jobID string) (string, error) {
	if err := ValidateChannelID(cid); err != nil {
		return "", err
	}
	if err := validateJobID(jobID); err != nil {
		return "", err
	}
	return SpoolFile(cid, jobID), nil
}

// Header renders the one-line human status header shared by `clown job status`
// and `ringmaster status`: "job <id> (<source>): <state>, elapsed <d>" with an
// optional ", last activity <ts>" suffix (RFC-0010 §3).
func (s Status) Header(jobID string) string {
	h := fmt.Sprintf("job %s (%s): %s, elapsed %s",
		jobID, s.Source, s.State, time.Duration(s.ElapsedSec)*time.Second)
	if s.LastActivity != "" {
		h += ", last activity " + s.LastActivity
	}
	return h
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
	return statusOfChannel(ChannelID(resolveSession(target)), jobID, tailN, now)
}

// StatusOfChannel is StatusOf addressed by an explicit channel id instead of a
// session key/target. It is the operator/cross-session path: a job discovered
// via `ringmaster ls --all` carries a (hashed, non-reversible) channel id but
// not its session key, so the session-key resolution StatusOf performs cannot
// reach it. cid is validated as a channel id (guarding the composed path);
// derivation is otherwise identical (see statusOfChannel). An invalid channel id
// wraps ErrInvalidChannelID.
func StatusOfChannel(cid, jobID string, tailN int, now time.Time) (Status, error) {
	if err := ValidateChannelID(cid); err != nil {
		return Status{}, err
	}
	return statusOfChannel(cid, jobID, tailN, now)
}

// statusOfChannel is StatusOf for an already-resolved channel id. It is the
// shared core both StatusOf (which resolves the channel from a session target)
// and the enumeration path (ListJobs/ListAllJobs, which already hold the cid)
// call, so status derivation lives in exactly one place.
func statusOfChannel(cid, jobID string, tailN int, now time.Time) (Status, error) {
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
		st.Tail, _ = SpoolTail(SpoolFile(cid, jobID), tailN)
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

// SpoolTail returns the last n lines of the spool at path together with the
// file's size, read from a bounded trailing window so the read never scales with
// spool size (RFC-0010 §3). A partial line at the window's leading edge is
// dropped. The returned size is the EOF offset a follow loop resumes from. A
// missing or unreadable spool yields (nil, 0). n<=0 returns every line in the
// window. It backs both the status-probe tail and `ringmaster tail`.
func SpoolTail(path string, n int) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0
	}
	size := info.Size()
	start := int64(0)
	if size > spoolTailWindow {
		start = size - spoolTailWindow
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, size
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, size
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:] // drop the partial first line inside the window
		}
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil, size
	}
	lines := strings.Split(trimmed, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, size
}

// StreamSpool copies any bytes appended to path past offset to w and returns the
// new EOF offset. A missing, shrunk, or unreadable spool is a no-op returning the
// input offset. It is the follow primitive behind `ringmaster tail -f`; like the
// rest of the follow path it is best-effort and swallows I/O errors so a
// transient read failure does not abort the stream.
func StreamSpool(w io.Writer, path string, offset int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= offset {
		return offset
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	if _, err := io.Copy(w, f); err != nil {
		return offset
	}
	return info.Size()
}

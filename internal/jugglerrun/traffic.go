package jugglerrun

import (
	"encoding/json"
	"os"
	"path/filepath"

	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// trafficSink appends one JSON line per jugglerloop.TrafficRecord to an open
// file (clown#255). A nil *trafficSink is a safe no-op: every method
// tolerates it, so a sink that could not be opened (or was never needed —
// the agent has no state directory) just means the run proceeds without
// traffic, not that it crashes.
type trafficSink struct {
	f      *os.File
	job    string
	logf   func(format string, args ...any)
	failed bool
}

// openTrafficSink opens path (job-keyed: see Store.TrafficPath), truncating
// any stale content from an earlier run of the SAME job (clown#255 review
// finding 8: O_APPEND alone would otherwise let a retried `juggler run --job
// X` on an adopted job append a second run's records after the first,
// restarting step numbering and breaking the positional join). This assumes
// the job's advisory lock is actually held for the run's whole duration, so
// no concurrent writer can race the truncation; when AcquireJobLock itself
// failed for a reason OTHER than "already locked" (RunAgent proceeds
// unlocked in that case, recording the loss in the ledger's Notes — see
// agent.go), that assumption does not hold, and a genuine concurrent
// duplicate `run --job X` could interleave writes with the live run's
// through the SAME *os.File — not a clean O_TRUNC takeover, since each
// process keeps its own file offset: the result is corrupted output
// (interleaved lines, NUL-holed where one writer's offset outran the
// other's) rather than a tidy "second run wins" (delta review finding 5;
// accepted — the same degraded, unlocked window already risks worse than
// this via the job's ledger and terminal record, so this one file is not
// special-cased). Creates the directory
// and the file if absent; job is stamped onto every record (delta review
// finding 1), so the file is self-identifying even moved out of its
// directory. It returns the sink plus the path to report
// (empty unless the file now genuinely exists). A failure to open is logged
// via logf and degrades to a nil sink; traffic is best-effort, never fatal
// to the run.
func openTrafficSink(path, job string, logf func(string, ...any)) (*trafficSink, string) {
	if path == "" {
		return nil, ""
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		logf("creating the traffic directory for %s: %v", path, err)
		return nil, ""
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		logf("opening traffic file %s: %v", path, err)
		return nil, ""
	}
	return &trafficSink{f: f, job: job, logf: logf}, path
}

// onTraffic is a jugglerloop.Config.OnTraffic value.
func (s *trafficSink) onTraffic(rec jugglerloop.TrafficRecord) {
	if s == nil {
		return
	}
	rec.Job = s.job
	line, err := json.Marshal(rec)
	if err != nil {
		s.logf("marshaling traffic record for %s: %v", rec.Tool, err)
		s.failed = true
		return
	}
	if _, err := s.f.Write(append(line, '\n')); err != nil {
		s.logf("writing traffic record for %s: %v", rec.Tool, err)
		s.failed = true
	}
}

// hadError reports whether any write (or the close) failed, so finish can
// record the loss in the ledger's Notes, not only stderr (clown#255 review
// finding 7, mirroring how a lock-acquire failure is recorded).
func (s *trafficSink) hadError() bool {
	return s != nil && s.failed
}

func (s *trafficSink) close() {
	if s == nil {
		return
	}
	if err := s.f.Close(); err != nil {
		s.logf("closing traffic file: %v", err)
		s.failed = true
	}
}

// trafficPathIfExists reports path only if a file is actually there
// (clown#255 review finding 4): the launcher side (spawn --wait) must not
// claim a traffic path the agent never got around to writing, e.g. a run
// that failed before anything was opened.
func trafficPathIfExists(path string) string {
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

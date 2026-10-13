package jugglerrun

import (
	"encoding/json"
	"os"
	"strings"

	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// trafficPathForSpool derives traffic.jsonl's path from the agent's
// ringmaster spool path (clown#255): the sibling file, the same way
// ringmaster's own lock-file convention derives from it (`${spool%.out}.lock`).
// An empty spoolPath (the job-platform facility disabled) yields "".
func trafficPathForSpool(spoolPath string) string {
	if spoolPath == "" {
		return ""
	}
	return strings.TrimSuffix(spoolPath, ".out") + ".traffic.jsonl"
}

// trafficSink appends one JSON line per jugglerloop.TrafficRecord to an open
// file (clown#255). A nil *trafficSink is a safe no-op: every method
// tolerates it, so a sink that could not be opened (or was never needed —
// the job-platform facility disabled) just means the run proceeds without
// traffic, not that it crashes.
type trafficSink struct {
	f    *os.File
	logf func(format string, args ...any)
}

// openTrafficSink opens path for append, creating it if absent, and returns
// the sink plus the path to report (empty unless the file now genuinely
// exists). A failure to open is logged via logf and degrades to a nil sink;
// traffic is best-effort, never fatal to the run.
func openTrafficSink(path string, logf func(string, ...any)) (*trafficSink, string) {
	if path == "" {
		return nil, ""
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logf("opening traffic file %s: %v", path, err)
		return nil, ""
	}
	return &trafficSink{f: f, logf: logf}, path
}

// onTraffic is a jugglerloop.Config.OnTraffic value.
func (s *trafficSink) onTraffic(rec jugglerloop.TrafficRecord) {
	if s == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		s.logf("marshaling traffic record for %s: %v", rec.Tool, err)
		return
	}
	if _, err := s.f.Write(append(line, '\n')); err != nil {
		s.logf("writing traffic record for %s: %v", rec.Tool, err)
	}
}

func (s *trafficSink) close() {
	if s == nil {
		return
	}
	if err := s.f.Close(); err != nil {
		s.logf("closing traffic file: %v", err)
	}
}

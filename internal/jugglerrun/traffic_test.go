package jugglerrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/jugglerloop"
)

func readTrafficLines(t *testing.T, path string) []jugglerloop.TrafficRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []jugglerloop.TrafficRecord
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec jugglerloop.TrafficRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parsing traffic line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// TestOpenTrafficSink_StampsJobOnEveryRecord covers clown#255 delta review
// finding 1's self-identifying requirement: onTraffic overwrites whatever
// Job the loop handed it (empty — jugglerloop has no notion of a job) with
// the sink's own.
func TestOpenTrafficSink_StampsJobOnEveryRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p", "job-1.jsonl")
	sink, got := openTrafficSink(path, "job-1", t.Logf)
	if sink == nil || got != path {
		t.Fatalf("openTrafficSink = %v, %q", sink, got)
	}
	sink.onTraffic(jugglerloop.TrafficRecord{Step: 1, Tool: "t", Job: "ignored-by-the-loop"})
	sink.close()
	lines := readTrafficLines(t, path)
	if len(lines) != 1 || lines[0].Job != "job-1" {
		t.Fatalf("lines = %+v, want job stamped job-1", lines)
	}
}

// TestOpenTrafficSink_TruncatesOnReopen covers clown#255 review finding 8
// (O_TRUNC, not O_APPEND): a retried `run --job X` on an adopted job must
// not append a second run's records after the first's.
func TestOpenTrafficSink_TruncatesOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-1.jsonl")
	first, _ := openTrafficSink(path, "job-1", t.Logf)
	first.onTraffic(jugglerloop.TrafficRecord{Step: 1, Tool: "first-run-only"})
	first.close()

	second, _ := openTrafficSink(path, "job-1", t.Logf)
	second.onTraffic(jugglerloop.TrafficRecord{Step: 1, Tool: "second-run"})
	second.close()

	lines := readTrafficLines(t, path)
	if len(lines) != 1 || lines[0].Tool != "second-run" {
		t.Fatalf("lines = %+v, want only the second run's single line", lines)
	}
}

// TestOpenTrafficSink_OpenFailureReturnsNilSinkAndEmptyPath: a directory
// that cannot be created (a regular file occupies a path component) must
// degrade to a nil sink and an empty path, never panic or error out to the
// caller — traffic is best-effort (clown#255).
func TestOpenTrafficSink_OpenFailureReturnsNilSinkAndEmptyPath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sink, path := openTrafficSink(filepath.Join(blocker, "principal", "job-1.jsonl"), "job-1", t.Logf)
	if sink != nil || path != "" {
		t.Fatalf("sink = %v, path = %q, want nil and empty", sink, path)
	}
}

// TestTrafficSink_NilIsASafeNoop: every method on a nil *trafficSink (the
// "traffic disabled or could not be opened" case) must be a no-op, never a
// nil-pointer panic.
func TestTrafficSink_NilIsASafeNoop(t *testing.T) {
	var s *trafficSink
	s.onTraffic(jugglerloop.TrafficRecord{Tool: "t"})
	s.close()
	if s.hadError() {
		t.Error("hadError on a nil sink must be false")
	}
}

// TestTrafficSink_WriteOrCloseFailureSetsHadError covers clown#255 review
// finding 7: a write (or close) failure must be observable via hadError,
// which agentRun.finish uses to leave a ledger note, not only a log line.
func TestTrafficSink_WriteOrCloseFailureSetsHadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-1.jsonl")
	sink, _ := openTrafficSink(path, "job-1", t.Logf)
	if sink.hadError() {
		t.Fatal("hadError must be false before any failure")
	}
	// Force the next write (and the eventual close) to fail, simulating a
	// mid-run I/O error without needing a real disk failure.
	sink.f.Close()
	sink.onTraffic(jugglerloop.TrafficRecord{Tool: "t"})
	if !sink.hadError() {
		t.Error("a write to a closed file must set hadError")
	}
	sink.close() // closing twice is itself an error; must stay a safe no-op
}

// TestTrafficPathIfExists covers clown#255 review finding 4: the launcher
// side (spawn --wait) must report a path only when the file is genuinely
// there, never a path the agent never got around to opening.
func TestTrafficPathIfExists(t *testing.T) {
	if got := trafficPathIfExists(""); got != "" {
		t.Errorf("empty path in = %q, want empty out", got)
	}
	missing := filepath.Join(t.TempDir(), "never-written.jsonl")
	if got := trafficPathIfExists(missing); got != "" {
		t.Errorf("missing file = %q, want empty", got)
	}
	present := filepath.Join(t.TempDir(), "written.jsonl")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := trafficPathIfExists(present); got != present {
		t.Errorf("present file = %q, want %q", got, present)
	}
}

package jobwake

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// mustReadJob parses a job's JSONL journal directly so producer tests do not
// depend on ReadJob's ordering before Task 6 lands. Once reader.go exists this
// could delegate to ReadJob; the inline parse keeps the helper self-contained.
func mustReadJob(t *testing.T, channelID, jobID string) []Record {
	t.Helper()
	f, err := os.Open(JournalFile(channelID, jobID))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unmarshal journal line %q: %v", line, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan journal: %v", err)
	}
	return out
}

func TestStartWritesStartedRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, err := Start(StartOpts{Source: "moxy", Label: "build"})
	if err != nil {
		t.Fatal(err)
	}
	recs := mustReadJob(t, ChannelID("k"), id)
	if len(recs) != 1 || recs[0].Type != TypeStarted || recs[0].Seq != 0 {
		t.Fatalf("want one started seq0 record, got %+v", recs)
	}
	if recs[0].Source != "moxy" || recs[0].Session != "k" || recs[0].V != 1 {
		t.Fatalf("bad record fields: %+v", recs[0])
	}
}

func TestProgressAndDoneIncrementSeq(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	if err := Progress(id, "halfway"); err != nil {
		t.Fatal(err)
	}
	if err := Done(id, TypeSucceeded, "ok", "ref"); err != nil {
		t.Fatal(err)
	}
	recs := mustReadJob(t, ChannelID("k"), id)
	if len(recs) != 3 || recs[1].Seq != 1 || recs[2].Seq != 2 {
		t.Fatalf("want seq 0,1,2; got %+v", recs)
	}
	if recs[2].Type != TypeSucceeded || recs[2].ResultRef != "ref" {
		t.Fatalf("bad terminal record: %+v", recs[2])
	}
	if recs[1].Source != "s" || recs[2].Source != "s" {
		t.Fatalf("source must carry forward, got %+v", recs)
	}
}

func TestDoneRejectsSecondTerminal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	if err := Done(id, TypeFailed, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Done(id, TypeSucceeded, "", ""); err == nil {
		t.Fatal("second terminal must error")
	}
}

func TestDoneRejectsBadState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	if err := Done(id, "wat", "", ""); err == nil {
		t.Fatal("non-terminal state must error")
	}
}

func TestProgressOneLineMessage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	if err := Progress(id, "line1\nline2\rline3"); err != nil {
		t.Fatal(err)
	}
	recs := mustReadJob(t, ChannelID("k"), id)
	if got := recs[1].Message; strings.ContainsAny(got, "\n\r") {
		t.Fatalf("message must be single-line, got %q", got)
	}
}

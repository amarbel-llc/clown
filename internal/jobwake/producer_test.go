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

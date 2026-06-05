package jobwake

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadJobSkipsMalformedLines(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"v":1,"job":"j","session":"k","source":"s","type":"started","seq":0,"ts":"t0"}
not json at all
{"v":1,"job":"j","session":"k","source":"s","type":"succeeded","seq":1,"ts":"t1"}

`
	if err := os.WriteFile(JournalFile(cid, "j"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadJob(cid, "j")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Type != TypeStarted || recs[1].Type != TypeSucceeded {
		t.Fatalf("want 2 valid records skipping garbage, got %+v", recs)
	}
}

func TestScanWakingReturnsOnlyTerminalSortedByTS(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	cid := ChannelID("k")

	a, _ := Start(StartOpts{Source: "s", Label: "a"})
	_ = Progress(a, "p")
	_ = Done(a, TypeSucceeded, "", "")

	b, _ := Start(StartOpts{Source: "s", Label: "b"})
	_ = Done(b, TypeFailed, "", "")

	// A job still in flight contributes no waking records.
	_, _ = Start(StartOpts{Source: "s", Label: "c"})

	waking, err := scanWaking(cid)
	if err != nil {
		t.Fatal(err)
	}
	if len(waking) != 2 {
		t.Fatalf("want 2 waking records, got %+v", waking)
	}
	for _, r := range waking {
		if !IsWaking(r.Type) {
			t.Fatalf("non-waking record leaked: %+v", r)
		}
	}
	if waking[0].TS > waking[1].TS {
		t.Fatalf("waking records must be sorted by ts, got %+v", waking)
	}
}

func TestScanWakingMissingDirIsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	waking, err := scanWaking(ChannelID("absent"))
	if err != nil {
		t.Fatalf("missing channel dir must not error: %v", err)
	}
	if len(waking) != 0 {
		t.Fatalf("want empty, got %+v", waking)
	}
}

func TestAckRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	a := loadAck(cid)
	if len(a.Acked) != 0 {
		t.Fatalf("missing ack must load empty, got %+v", a)
	}
	a.Acked["job1"] = 2
	if err := saveAck(cid, a); err != nil {
		t.Fatal(err)
	}
	back := loadAck(cid)
	if back.Acked["job1"] != 2 {
		t.Fatalf("ack did not round-trip, got %+v", back)
	}
}

func TestLoadAckCorruptIsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(AckFile(cid), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := loadAck(cid)
	if len(a.Acked) != 0 {
		t.Fatalf("corrupt ack must load empty, got %+v", a)
	}
}

func TestScanWakingSkipsDotfiles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	cid := ChannelID("k")
	id, _ := Start(StartOpts{Source: "s"})
	_ = Done(id, TypeSucceeded, "", "")
	// Drop an ack file alongside; scanWaking must not treat .ack.json as a job.
	if err := saveAck(cid, ack{V: 1, Acked: map[string]int{id: 0}}); err != nil {
		t.Fatal(err)
	}
	// Also a non-jsonl file should be ignored.
	if err := os.WriteFile(filepath.Join(JournalDir(cid), "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	waking, err := scanWaking(cid)
	if err != nil {
		t.Fatal(err)
	}
	if len(waking) != 1 {
		t.Fatalf("want exactly the one job's terminal record, got %+v", waking)
	}
}

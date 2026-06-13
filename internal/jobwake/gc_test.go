package jobwake

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJournal creates a minimal journal file for jobID in the channel dir
// and returns its path.
func writeJournal(t *testing.T, cid, jobID string) string {
	t.Helper()
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	p := JournalFile(cid, jobID)
	if err := os.WriteFile(p, []byte(`{"v":1,"job":"`+jobID+`","type":"succeeded","seq":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// age sets a file's mtime to older than the retention window.
func age(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-journalRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// ageBy sets a file's mtime to d in the past.
func ageBy(t *testing.T, path string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestSweepReapsAgedJournalsKeepsFresh(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")
	aged := writeJournal(t, cid, "old-job")
	fresh := writeJournal(t, cid, "new-job")
	age(t, aged)

	sweep(cid, time.Now())

	if _, err := os.Stat(aged); !os.IsNotExist(err) {
		t.Errorf("aged journal must be reaped, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh journal must be kept, stat err = %v", err)
	}
}

func TestSweepPrunesAckEntriesOnlyForMissingJournals(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")
	writeJournal(t, cid, "alive")
	if err := saveAckPath(AckFile(cid), ack{V: 1, Acked: map[string]int{"alive": 3, "gone": 7}}); err != nil {
		t.Fatal(err)
	}

	sweep(cid, time.Now())

	a := loadAckPath(AckFile(cid))
	if _, ok := a.Acked["alive"]; !ok {
		t.Error("ack entry whose journal still exists must never be pruned (would re-emit)")
	}
	if _, ok := a.Acked["gone"]; ok {
		t.Error("ack entry for a missing journal must be pruned")
	}
}

// The ordering subtlety: a journal aged past retention is reaped first, and
// its ack entry is pruned in the SAME sweep (acks are pruned after journal
// removal).
func TestSweepReapsAgedJournalAndItsAckEntry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")
	aged := writeJournal(t, cid, "old-job")
	age(t, aged)
	if err := saveAckPath(AckFile(cid), ack{V: 1, Acked: map[string]int{"old-job": 2}}); err != nil {
		t.Fatal(err)
	}

	sweep(cid, time.Now())

	a := loadAckPath(AckFile(cid))
	if _, ok := a.Acked["old-job"]; ok {
		t.Error("ack entry for a journal reaped in the same sweep must be pruned")
	}
}

func TestSweepReapsStaleForeignReaderAcksKeepsFreshAndOwn(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	readerCID := ChannelID("me")
	bcid := ChannelID(BroadcastKey)
	if err := os.MkdirAll(JournalDir(bcid), 0o700); err != nil {
		t.Fatal(err)
	}

	own := AckFileFor(bcid, readerCID)
	staleForeign := AckFileFor(bcid, ChannelID("dead-session"))
	freshForeign := AckFileFor(bcid, ChannelID("live-session"))
	for _, p := range []string{own, staleForeign, freshForeign} {
		if err := saveAckPath(p, ack{V: 1, Acked: map[string]int{}}); err != nil {
			t.Fatal(err)
		}
	}
	age(t, staleForeign)
	age(t, own) // even an aged own ack must survive: it is about to be refreshed

	sweep(readerCID, time.Now())

	if _, err := os.Stat(staleForeign); !os.IsNotExist(err) {
		t.Errorf("stale foreign reader ack must be reaped, stat err = %v", err)
	}
	if _, err := os.Stat(freshForeign); err != nil {
		t.Errorf("fresh foreign reader ack must be kept, stat err = %v", err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Errorf("own reader ack must be kept, stat err = %v", err)
	}
}

func TestSweepNoopOnEmptyOrMissingDirs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")

	// Missing dirs: must not panic or create anything.
	sweep(cid, time.Now())

	// Empty channel dir, no ack file: sweep must not conjure an ack file —
	// creating the broadcast per-reader ack would corrupt first-attach
	// (condvar init-at-end) semantics.
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	sweep(cid, time.Now())
	if _, err := os.Stat(AckFile(cid)); !os.IsNotExist(err) {
		t.Errorf("sweep must not create the channel ack file, stat err = %v", err)
	}
	bcid := ChannelID(BroadcastKey)
	if _, err := os.Stat(AckFileFor(bcid, cid)); !os.IsNotExist(err) {
		t.Errorf("sweep must not create the broadcast per-reader ack file, stat err = %v", err)
	}
}

// Watch wires the sweep at monitor start: an aged journal present before
// Watch begins is gone after the watch cycle.
func TestWatchRunsSweepAtStart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	cid := ChannelID("k")
	aged := writeJournal(t, cid, "ancient")
	age(t, aged)

	_ = drainWatch(t, "k")

	if _, err := os.Stat(aged); !os.IsNotExist(err) {
		t.Errorf("Watch must sweep aged journals at start, stat err = %v", err)
	}
}

// A reaped journal's events must not re-emit: the ack prune only ever drops
// entries whose journal is gone, so nothing in the remaining journals becomes
// unacked. Pin it end-to-end: emit+ack a job, age its journal, sweep, then
// replay — nothing emits.
func TestSweepDoesNotCauseReemit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, err := Start(StartOpts{Source: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Done("", id, TypeSucceeded, "ok", ""); err != nil {
		t.Fatal(err)
	}
	cid := ChannelID("k")
	var first []Record
	if err := ReplayOnce("k", func(r Record) error { first = append(first, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("setup: want one emit, got %+v", first)
	}

	age(t, JournalFile(cid, id))
	sweep(cid, time.Now())

	var second []Record
	if err := ReplayOnce("k", func(r Record) error { second = append(second, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("post-sweep replay must emit nothing, got %+v", second)
	}
}

// A delivered standalone message on the owning channel is reaped the instant it
// is acked (RFC-0010 §4): its wake is the whole job, so it leaves no msg-* row.
func TestDeliveredMessageReapedOnOwnChannel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, err := Message("", "s", "", "ping", "")
	if err != nil {
		t.Fatal(err)
	}
	cid := ChannelID("k")
	if _, err := os.Stat(JournalFile(cid, id)); err != nil {
		t.Fatalf("setup: message journal should exist, err = %v", err)
	}

	var got []Record
	if err := ReplayOnce("k", func(r Record) error { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != TypeMessage {
		t.Fatalf("want one message emit, got %+v", got)
	}
	if _, err := os.Stat(JournalFile(cid, id)); !os.IsNotExist(err) {
		t.Errorf("delivered message journal must be reaped on own-channel ack, stat err = %v", err)
	}
}

// A delivered broadcast message is NOT reaped on one reader's ack: readers share
// the journal and a second reader may not have acked yet (RFC-0010 §4).
func TestBroadcastMessageNotReapedOnOneReaderAck(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "sender")

	for _, r := range []string{"reader-1", "reader-2"} {
		if got := replayBroadcast(t, r); len(got) != 0 {
			t.Fatalf("%s first attach must emit nothing, got %+v", r, got)
		}
	}
	id, err := Message(BroadcastKey, "s", "sender", "to all", "")
	if err != nil {
		t.Fatal(err)
	}
	bcid := ChannelID(BroadcastKey)

	if got := replayBroadcast(t, "reader-1"); len(got) != 1 || got[0].Job != id {
		t.Fatalf("reader-1 must receive the broadcast, got %+v", got)
	}
	if _, err := os.Stat(JournalFile(bcid, id)); err != nil {
		t.Fatalf("broadcast message journal must survive one reader's ack, stat err = %v", err)
	}
	if got := replayBroadcast(t, "reader-2"); len(got) != 1 || got[0].Job != id {
		t.Fatalf("reader-2 must still receive the broadcast, got %+v", got)
	}
}

// Resting-retention tier (RFC-0010 §4): a terminal job whose terminal record is
// acked is reaped after restingRetention, well before the journalRetention
// backstop; an unacked terminal and an orphaned never-terminal job both survive
// the resting window and wait for the 7d backstop.
func TestSweepRestingRetentionForDeliveredTerminals(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")

	delivered := writeJournal(t, cid, "delivered-job")     // seq-1 succeeded record
	undelivered := writeJournal(t, cid, "undelivered-job") // same shape, never acked
	orphan := JournalFile(cid, "orphan-job")               // started, no terminal
	if err := os.WriteFile(orphan, []byte(`{"v":1,"job":"orphan-job","type":"started","seq":0}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveAckPath(AckFile(cid), ack{V: 1, Acked: map[string]int{"delivered-job": 1}}); err != nil {
		t.Fatal(err)
	}

	restAge := restingRetention + time.Hour // past resting, well under journalRetention
	ageBy(t, delivered, restAge)
	ageBy(t, undelivered, restAge)
	ageBy(t, orphan, restAge)

	sweep(cid, time.Now())

	if _, err := os.Stat(delivered); !os.IsNotExist(err) {
		t.Errorf("delivered terminal past restingRetention must be reaped, stat err = %v", err)
	}
	if _, err := os.Stat(undelivered); err != nil {
		t.Errorf("undelivered terminal must survive the resting window (backstop only), stat err = %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("orphaned running job must survive the resting window (backstop only), stat err = %v", err)
	}
}

func TestSweepIgnoresDotfilesAndNonJournals(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cid := ChannelID("k")
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(JournalDir(cid), "notes.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveAckPath(AckFile(cid), ack{V: 1, Acked: map[string]int{}}); err != nil {
		t.Fatal(err)
	}
	age(t, stray)
	age(t, AckFile(cid))

	sweep(cid, time.Now())

	if _, err := os.Stat(stray); err != nil {
		t.Errorf("non-journal files must not be touched, stat err = %v", err)
	}
	if _, err := os.Stat(AckFile(cid)); err != nil {
		t.Errorf("the channel's own ack dotfile must not be reaped, stat err = %v", err)
	}
}

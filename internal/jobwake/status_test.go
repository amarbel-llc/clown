package jobwake

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

// SpoolPath returns the .out sibling of the journal, creates the channel dir,
// and must NOT create the spool file itself (RFC-0010 §2).
func TestSpoolPathShapeAndNoCreate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	cid := ChannelID("k")

	got, err := SpoolPath("", "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := SpoolFile(cid, "build-1"); got != want {
		t.Fatalf("spool path: want %q, got %q", want, got)
	}
	if info, err := os.Stat(JournalDir(cid)); err != nil || !info.IsDir() {
		t.Fatalf("channel dir not created: %v", err)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("spool file must not be created by spool-path, stat err = %v", err)
	}
}

func TestSpoolPathRejectsInvalidID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	for _, id := range []string{"..", "../evil", "a/b"} {
		if _, err := SpoolPath("", id); !errors.Is(err, ErrInvalidJobID) {
			t.Errorf("SpoolPath(%q) err = %v, want ErrInvalidJobID", id, err)
		}
	}
}

// A running job: state=running, no ended, elapsed = now-started, and with no
// spool last_activity is the newest journal record's ts verbatim (RFC-0010 §3).
func TestStatusOfRunning(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, err := Start(StartOpts{Source: "moxy", Label: "build"})
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := ReadJob(ChannelID("k"), id)
	now := parseTS(recs[0].TS).Add(90 * time.Second)

	st, err := StatusOf("", id, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "running" || st.Ended != "" {
		t.Fatalf("want running with no ended, got %+v", st)
	}
	if st.Source != "moxy" || st.Started != recs[0].TS {
		t.Fatalf("bad source/started: %+v", st)
	}
	if st.ElapsedSec != 90 {
		t.Fatalf("elapsed: want 90, got %d", st.ElapsedSec)
	}
	if st.LastActivity != recs[len(recs)-1].TS {
		t.Fatalf("last_activity: want newest journal ts %q, got %q", recs[len(recs)-1].TS, st.LastActivity)
	}
	if st.SpoolBytes != 0 || st.Tail != nil {
		t.Fatalf("no spool expected, got bytes=%d tail=%v", st.SpoolBytes, st.Tail)
	}
}

// A terminal job: state is the terminal type, ended is its ts, progress carries
// the newest progress message.
func TestStatusOfTerminal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "spinclass"})
	if err := Progress("", id, "halfway"); err != nil {
		t.Fatal(err)
	}
	if err := Done("", id, TypeSucceeded, "ok", "ref"); err != nil {
		t.Fatal(err)
	}
	recs, _ := ReadJob(ChannelID("k"), id)

	st, err := StatusOf("", id, 20, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != TypeSucceeded || st.Ended != recs[len(recs)-1].TS {
		t.Fatalf("want succeeded with ended=terminal ts, got %+v", st)
	}
	if st.Progress != "halfway" {
		t.Fatalf("progress: want halfway, got %q", st.Progress)
	}
	if st.ElapsedSec < 0 {
		t.Fatalf("elapsed must be >= 0, got %d", st.ElapsedSec)
	}
}

// A standalone message job: a single self-contained `message` record (no
// started, no terminal) rests at `delivered`, not `running`. ended is the
// message ts and elapsed is 0 — never the unbounded now-started a running job
// reports (the RFC-0009 §4 carve-out).
func TestStatusOfDeliveredMessage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "k")

	id, err := Message("", "spinclass", "", "ping", "ref")
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := ReadJob(ChannelID("k"), id)
	// now is far past the message ts; elapsed must still be 0, proving it is
	// derived from ended (the message ts), not now-started.
	now := parseTS(recs[0].TS).Add(72 * time.Hour)

	st, err := StatusOf("", id, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateDelivered {
		t.Fatalf("state: want %q, got %q", StateDelivered, st.State)
	}
	if st.Started != recs[0].TS || st.Ended != recs[0].TS {
		t.Fatalf("started/ended must both be the message ts, got %+v", st)
	}
	if st.ElapsedSec != 0 {
		t.Fatalf("elapsed: want 0 (delivered, not running), got %d", st.ElapsedSec)
	}
	if st.Source != "spinclass" {
		t.Fatalf("source: want spinclass, got %q", st.Source)
	}
}

// With a spool present: spool_bytes is its size, tail is the last N lines from
// the bounded window, and last_activity is max(spool mtime, journal ts) — here
// the spool mtime is forced newer (RFC-0010 §3).
func TestStatusSpoolTailAndLastActivity(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "moxy"})

	sp, err := SpoolPath("", id)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("a\nb\nc\nd\n")
	if err := os.WriteFile(sp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(sp, future, future); err != nil {
		t.Fatal(err)
	}

	st, err := StatusOf("", id, 2, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if st.SpoolBytes != int64(len(content)) {
		t.Fatalf("spool_bytes: want %d, got %d", len(content), st.SpoolBytes)
	}
	if want := []string{"c", "d"}; !reflect.DeepEqual(st.Tail, want) {
		t.Fatalf("tail: want %v, got %v", want, st.Tail)
	}
	if la := parseTS(st.LastActivity); la.Unix() != future.Unix() {
		t.Fatalf("last_activity: want spool mtime %v, got %q", future, st.LastActivity)
	}
}

func TestStatusOfMissingJournal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	if _, err := StatusOf("", "nope-12345678", 20, time.Now().UTC()); !os.IsNotExist(err) {
		t.Fatalf("missing journal: want os.IsNotExist, got %v", err)
	}
}

func TestStatusOfRejectsInvalidID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	if _, err := StatusOf("", "../passwd", 20, time.Now().UTC()); !errors.Is(err, ErrInvalidJobID) {
		t.Fatalf("invalid id: want ErrInvalidJobID, got %v", err)
	}
}

// GC reaps a job's spool together with its aged journal, even when the spool's
// own mtime is fresh (RFC-0010 §4: the spool dies with its journal).
func TestSweepReapsSpoolWithJournal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	cid := ChannelID("k")
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	jf, sp := JournalFile(cid, "old-1"), SpoolFile(cid, "old-1")
	if err := os.WriteFile(jf, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte("live output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(jf, aged, aged); err != nil { // journal aged; spool mtime stays fresh
		t.Fatal(err)
	}

	sweep(cid, time.Now())

	if _, err := os.Stat(jf); !os.IsNotExist(err) {
		t.Fatalf("aged journal not reaped: %v", err)
	}
	if _, err := os.Stat(sp); !os.IsNotExist(err) {
		t.Fatalf("spool not reaped with its journal: %v", err)
	}
}

// GC's orphan-spool sweep is age-gated: an aged orphan .out (no journal) is
// reaped, a fresh one is retained (RFC-0010 §4 — protects a spool created by
// spool-path before its started journal lands).
func TestSweepOrphanSpoolAgeGated(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	cid := ChannelID("k")
	if err := os.MkdirAll(JournalDir(cid), 0o700); err != nil {
		t.Fatal(err)
	}
	oldSp, freshSp := SpoolFile(cid, "orphan-old"), SpoolFile(cid, "orphan-new")
	if err := os.WriteFile(oldSp, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freshSp, []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldSp, aged, aged); err != nil {
		t.Fatal(err)
	}

	sweep(cid, time.Now())

	if _, err := os.Stat(oldSp); !os.IsNotExist(err) {
		t.Fatalf("aged orphan spool not reaped: %v", err)
	}
	if _, err := os.Stat(freshSp); err != nil {
		t.Fatalf("fresh orphan spool must be retained: %v", err)
	}
}

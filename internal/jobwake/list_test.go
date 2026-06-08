package jobwake

import (
	"testing"
	"time"
)

// ListJobs enumerates every job in the resolved channel with its derived status,
// oldest first by start time, and carries no spool tail (tailN 0).
func TestListJobsOrdersAndSummarizes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")

	older, _ := Start(StartOpts{Source: "moxy", Label: "first"})
	// Force a strictly later started ts so the sort is deterministic regardless
	// of clock resolution: the second Start runs after a tiny sleep.
	time.Sleep(2 * time.Millisecond)
	newer, _ := Start(StartOpts{Source: "spinclass", Label: "second"})
	if err := Done("", newer, TypeSucceeded, "ok", ""); err != nil {
		t.Fatal(err)
	}

	rows, err := ListJobs("", 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].JobID != older || rows[1].JobID != newer {
		t.Fatalf("order: want [%s %s], got [%s %s]", older, newer, rows[0].JobID, rows[1].JobID)
	}
	if rows[0].Status.State != "running" {
		t.Fatalf("first job state: want running, got %q", rows[0].Status.State)
	}
	if rows[1].Status.State != TypeSucceeded {
		t.Fatalf("second job state: want succeeded, got %q", rows[1].Status.State)
	}
	if rows[0].Status.Source != "moxy" {
		t.Fatalf("first job source: want moxy, got %q", rows[0].Status.Source)
	}
	if rows[0].Channel != "" {
		t.Fatalf("single-channel listing must not tag a channel, got %q", rows[0].Channel)
	}
}

// ListJobs on a session with no channel dir is an empty listing, not an error.
func TestListJobsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	rows, err := ListJobs("", 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("want empty, got %+v", rows)
	}
}

// ListAllJobs spans every channel under JobsRoot and tags each row with its
// channel id, so an operator sees jobs from sessions it does not hold the key
// for.
func TestListAllJobsSpansChannels(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	t.Setenv("CLOWN_SESSION_ID", "session-a")
	a, _ := Start(StartOpts{Source: "moxy"})
	t.Setenv("CLOWN_SESSION_ID", "session-b")
	b, _ := Start(StartOpts{Source: "spinclass"})

	rows, err := ListAllJobs(0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows across channels, got %d: %+v", len(rows), rows)
	}
	found := map[string]string{} // jobID -> channel
	for _, r := range rows {
		if r.Channel == "" {
			t.Fatalf("cross-channel row missing channel tag: %+v", r)
		}
		found[r.JobID] = r.Channel
	}
	if found[a] != ChannelID("session-a") {
		t.Fatalf("job a channel: want %s, got %s", ChannelID("session-a"), found[a])
	}
	if found[b] != ChannelID("session-b") {
		t.Fatalf("job b channel: want %s, got %s", ChannelID("session-b"), found[b])
	}
}

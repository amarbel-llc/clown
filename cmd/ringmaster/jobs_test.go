package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// captureStdout swaps os.Stdout for a pipe, runs fn, and returns everything fn
// wrote. The subcommands print results directly to os.Stdout, so this is how the
// tests observe them.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// jobEnv points the jobwake channel at a temp state dir bound to a fixed session
// key, so each test gets an isolated set of journals.
func jobEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
}

func TestRunUnknownCommand(t *testing.T) {
	if code := run([]string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown command: want exit 2, got %d", code)
	}
}

func TestRunHelp(t *testing.T) {
	out := captureStdout(t, func() {
		if code := run([]string{"--help"}); code != 0 {
			t.Fatalf("--help: want exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "ls ") || !strings.Contains(out, "cancel ") {
		t.Fatalf("help should list job subcommands, got:\n%s", out)
	}
}

func TestLsEmpty(t *testing.T) {
	jobEnv(t)
	out := captureStdout(t, func() {
		if code := run([]string{"ls"}); code != 0 {
			t.Fatalf("ls empty: want exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "no jobs" {
		t.Fatalf("ls empty: want 'no jobs', got %q", out)
	}
}

func TestLsListsJobs(t *testing.T) {
	jobEnv(t)
	id, err := jobwake.Start(jobwake.StartOpts{Source: "moxy", Label: "build"})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := run([]string{"ls"}); code != 0 {
			t.Fatalf("ls: want exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, id) || !strings.Contains(out, "moxy") || !strings.Contains(out, "running") {
		t.Fatalf("ls output missing job/source/state, got:\n%s", out)
	}
	if !strings.Contains(out, "JOB") {
		t.Fatalf("ls output missing header, got:\n%s", out)
	}
}

func TestLsJSON(t *testing.T) {
	jobEnv(t)
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	out := captureStdout(t, func() {
		if code := run([]string{"ls", "--json"}); code != 0 {
			t.Fatalf("ls --json: want exit 0, got %d", code)
		}
	})
	var rows []jobwake.JobSummary
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("ls --json not valid JSON array: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0].JobID != id {
		t.Fatalf("ls --json: want one row for %s, got %+v", id, rows)
	}
}

func TestStatusMissingAndInvalid(t *testing.T) {
	jobEnv(t)
	if code := run([]string{"status"}); code != 2 {
		t.Fatalf("status without id: want exit 2, got %d", code)
	}
	if code := run([]string{"status", "nope-12345678"}); code != 1 {
		t.Fatalf("status missing job: want exit 1, got %d", code)
	}
	if code := run([]string{"status", "../passwd"}); code != 2 {
		t.Fatalf("status invalid id: want exit 2, got %d", code)
	}
}

func TestStatusJSON(t *testing.T) {
	jobEnv(t)
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "spinclass"})
	out := captureStdout(t, func() {
		if code := run([]string{"status", id, "--json"}); code != 0 {
			t.Fatalf("status --json: want exit 0, got %d", code)
		}
	})
	var st jobwake.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --json not valid: %v\n%s", err, out)
	}
	if st.State != "running" || st.Source != "spinclass" {
		t.Fatalf("status --json: got %+v", st)
	}
}

func TestTailNonFollow(t *testing.T) {
	jobEnv(t)
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	sp, err := jobwake.SpoolPath("", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := run([]string{"tail", id, "-n", "2"}); code != 0 {
			t.Fatalf("tail: want exit 0, got %d", code)
		}
	})
	if strings.Contains(out, "line1") {
		t.Fatalf("tail -n 2 should drop line1, got:\n%s", out)
	}
	if !strings.Contains(out, "line2") || !strings.Contains(out, "line3") {
		t.Fatalf("tail -n 2 should show last two lines, got:\n%s", out)
	}
}

// tail on a job that does not exist in the resolved channel errors (exit 1)
// rather than silently printing nothing at exit 0. This covers the #128 trap:
// an id copied from `ls --all` but tailed without --channel resolves to the
// current session's channel and previously read an empty/absent spool in
// silence. A missing job mirrors `status`; a malformed id is still a usage
// error (exit 2).
func TestTailMissingAndInvalid(t *testing.T) {
	jobEnv(t)
	if code := run([]string{"tail"}); code != 2 {
		t.Fatalf("tail without id: want exit 2, got %d", code)
	}
	if code := run([]string{"tail", "nope-12345678"}); code != 1 {
		t.Fatalf("tail missing job: want exit 1, got %d", code)
	}
	if code := run([]string{"tail", "../passwd"}); code != 2 {
		t.Fatalf("tail invalid id: want exit 2, got %d", code)
	}
}

// A foreign-channel job tailed WITHOUT --channel resolves to the operator's own
// session, where the job does not exist, and must error (exit 1) instead of
// silently succeeding — the operator-visible half of #128.
func TestTailForeignWithoutChannelErrors(t *testing.T) {
	id, _ := foreignJob(t)
	if code := run([]string{"tail", id}); code != 1 {
		t.Fatalf("tail foreign job without --channel: want exit 1, got %d", code)
	}
}

// tail -f streams output appended after the job starts and returns once the job
// reaches a terminal state.
func TestTailFollowStopsOnTerminal(t *testing.T) {
	jobEnv(t)
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	sp, _ := jobwake.SpoolPath("", id)
	if err := os.WriteFile(sp, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Append more output, then terminate the job, while the follow loop runs.
	go func() {
		time.Sleep(120 * time.Millisecond)
		f, _ := os.OpenFile(sp, os.O_WRONLY|os.O_APPEND, 0o600)
		_, _ = f.WriteString("second\n")
		_ = f.Close()
		time.Sleep(120 * time.Millisecond)
		_ = jobwake.Done("", id, jobwake.TypeSucceeded, "ok", "")
	}()

	done := make(chan string, 1)
	go func() {
		done <- captureStdout(t, func() {
			run([]string{"tail", id, "-f", "-n", "10"})
		})
	}()

	select {
	case out := <-done:
		if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
			t.Fatalf("tail -f should show both writes, got:\n%s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tail -f did not return after the job went terminal")
	}
}

func TestCancelWritesTerminalRecord(t *testing.T) {
	jobEnv(t)
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})

	out := captureStdout(t, func() {
		if code := run([]string{"cancel", id}); code != 0 {
			t.Fatalf("cancel: want exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, id) {
		t.Fatalf("cancel should print the job id, got %q", out)
	}

	st, err := jobwake.StatusOf("", id, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != jobwake.TypeCancelled {
		t.Fatalf("cancel should write a cancelled terminal record, got state %q", st.State)
	}
}

func TestCancelMissingAndDouble(t *testing.T) {
	jobEnv(t)
	if code := run([]string{"cancel"}); code != 2 {
		t.Fatalf("cancel without id: want exit 2, got %d", code)
	}
	if code := run([]string{"cancel", "nope-12345678"}); code != 1 {
		t.Fatalf("cancel missing job: want exit 1, got %d", code)
	}

	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	if code := run([]string{"cancel", id}); code != 0 {
		t.Fatalf("first cancel: want exit 0, got %d", code)
	}
	if code := run([]string{"cancel", id}); code != 1 {
		t.Fatalf("second cancel on terminal job: want exit 1, got %d", code)
	}
}

// foreignJob starts a job under session "owner" and then rebinds the current
// session to "operator", returning the job id and owner's channel id. This is
// the #125 setup: an operator at a terminal it does not own the session key for,
// reaching a job only by the channel id `ls --all` prints.
func foreignJob(t *testing.T) (id, ownerChannel string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "owner")
	id, err := jobwake.Start(jobwake.StartOpts{Source: "moxy", Label: "build"})
	if err != nil {
		t.Fatal(err)
	}
	ownerChannel = jobwake.ChannelID("owner")
	t.Setenv("CLOWN_SESSION_ID", "operator")
	return id, ownerChannel
}

// status --channel reaches a job in a session the operator does not hold the key
// for, while the default (operator-session) lookup cannot see it.
func TestStatusByChannel(t *testing.T) {
	id, ch := foreignJob(t)

	out := captureStdout(t, func() {
		if code := run([]string{"status", id, "--channel", ch, "--json"}); code != 0 {
			t.Fatalf("status --channel: want exit 0, got %d", code)
		}
	})
	var st jobwake.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --channel --json invalid: %v\n%s", err, out)
	}
	if st.State != "running" || st.Source != "moxy" {
		t.Fatalf("status --channel: got %+v", st)
	}

	// Without --channel the operator session can't see the foreign job.
	if code := run([]string{"status", id}); code != 1 {
		t.Fatalf("status without channel from foreign session: want exit 1, got %d", code)
	}
}

// tail --channel streams a foreign-channel job's spool.
func TestTailByChannel(t *testing.T) {
	id, ch := foreignJob(t)
	sp, err := jobwake.SpoolPath("owner", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := run([]string{"tail", id, "--channel", ch, "-n", "2"}); code != 0 {
			t.Fatalf("tail --channel: want exit 0, got %d", code)
		}
	})
	if strings.Contains(out, "a") || !strings.Contains(out, "b") || !strings.Contains(out, "c") {
		t.Fatalf("tail --channel -n 2: want last two lines, got:\n%s", out)
	}
}

// cancel --channel writes the terminal record for a foreign-channel job.
func TestCancelByChannel(t *testing.T) {
	id, ch := foreignJob(t)

	out := captureStdout(t, func() {
		if code := run([]string{"cancel", id, "--channel", ch}); code != 0 {
			t.Fatalf("cancel --channel: want exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, id) {
		t.Fatalf("cancel --channel should print the job id, got %q", out)
	}
	st, err := jobwake.StatusOfChannel(ch, id, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != jobwake.TypeCancelled {
		t.Fatalf("cancel --channel: want cancelled, got %q", st.State)
	}
}

// --target and --channel are mutually exclusive; supplying both is a usage error.
func TestTargetChannelMutuallyExclusive(t *testing.T) {
	jobEnv(t)
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	ch := jobwake.ChannelID("k")
	for _, verb := range [][]string{
		{"status", id, "--target", "k", "--channel", ch},
		{"tail", id, "--target", "k", "--channel", ch},
		{"cancel", id, "--target", "k", "--channel", ch},
	} {
		if code := run(verb); code != 2 {
			t.Fatalf("%v: want exit 2 for --target+--channel, got %d", verb, code)
		}
	}
}

// A malformed --channel is a usage error (exit 2), like a malformed job id.
func TestChannelInvalid(t *testing.T) {
	jobEnv(t)
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	if code := run([]string{"status", id, "--channel", "../etc"}); code != 2 {
		t.Fatalf("status --channel ../etc: want exit 2, got %d", code)
	}
}

// ls --all prints the full channel id (not a truncated prefix), so the value is
// the exact one status/tail/cancel --channel take.
func TestLsAllPrintsFullChannel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "owner")
	id, _ := jobwake.Start(jobwake.StartOpts{Source: "moxy"})
	full := jobwake.ChannelID("owner")

	out := captureStdout(t, func() {
		if code := run([]string{"ls", "--all"}); code != 0 {
			t.Fatalf("ls --all: want exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, full) {
		t.Fatalf("ls --all should print full channel id %s, got:\n%s", full, out)
	}
	if !strings.Contains(out, id) {
		t.Fatalf("ls --all should list the job %s, got:\n%s", id, out)
	}
}

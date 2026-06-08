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

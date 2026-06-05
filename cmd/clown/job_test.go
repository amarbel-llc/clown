package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn wrote. It restores os.Stdout before returning.
//
// WARNING: this mutates the process-global os.Stdout without synchronization.
// Tests that call captureStdout MUST NOT call t.Parallel() — concurrent tests
// would race on os.Stdout and interleave each other's captures.
func captureStdout(t *testing.T, fn func() int) string {
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

func trimTrailingNewline(s string) string {
	return strings.TrimRight(s, "\n")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func TestNotificationLine(t *testing.T) {
	tests := []struct {
		name string
		rec  jobwake.Record
		want string
	}{
		{
			name: "message and result_ref",
			rec:  jobwake.Record{Source: "moxy", Job: "build-3f2a", Type: jobwake.TypeSucceeded, Message: "nix build ok", ResultRef: "moxy job-read --job build-3f2a"},
			want: "[clown-job] moxy build-3f2a succeeded: nix build ok · moxy job-read --job build-3f2a",
		},
		{
			name: "message no result_ref",
			rec:  jobwake.Record{Source: "spinclass", Job: "merge-1", Type: jobwake.TypeFailed, Message: "conflict"},
			want: "[clown-job] spinclass merge-1 failed: conflict",
		},
		{
			name: "no message omits colon",
			rec:  jobwake.Record{Source: "moxy", Job: "j1", Type: jobwake.TypeCancelled},
			want: "[clown-job] moxy j1 cancelled",
		},
		{
			name: "no message but result_ref",
			rec:  jobwake.Record{Source: "moxy", Job: "j2", Type: jobwake.TypeInterrupted, ResultRef: "ref"},
			want: "[clown-job] moxy j2 interrupted · ref",
		},
		{
			name: "embedded newline in message is stripped to space",
			rec:  jobwake.Record{Source: "s", Job: "j3", Type: jobwake.TypeSucceeded, Message: "line1\nline2"},
			want: "[clown-job] s j3 succeeded: line1 line2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := notificationLine(tc.rec); got != tc.want {
				t.Fatalf("notificationLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

// jobTestEnv isolates the journal + a short runtime dir per test. The runtime
// dir must be short (AF_UNIX sun_path is ~108 bytes) so live job-watch nudges
// bind; the deep worktree .tmp would overflow it, so we use /tmp directly.
func jobTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, err := os.MkdirTemp("/tmp", "clown-jobtest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("CLOWN_SESSION_ID", "repo/branch")
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
}

func TestJobStartPrintsIDAndWritesRecord(t *testing.T) {
	jobTestEnv(t)
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "moxy", "--label", "build"}) })
	id := trimTrailingNewline(out)
	if id == "" {
		t.Fatal("job start printed no id")
	}
	recs, err := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Type != jobwake.TypeStarted || recs[0].Source != "moxy" || recs[0].Seq != 0 {
		t.Fatalf("want one started seq0 moxy record, got %+v", recs)
	}
}

func TestJobDoneWritesTerminalRecord(t *testing.T) {
	jobTestEnv(t)
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := jobDone([]string{id, "--state", "succeeded", "--message", "ok", "--result-ref", "ref"}); code != 0 {
		t.Fatalf("job done exit = %d, want 0", code)
	}
	recs, err := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[1].Type != jobwake.TypeSucceeded || recs[1].ResultRef != "ref" {
		t.Fatalf("bad terminal record: %+v", recs)
	}
}

func TestJobDoneBadStateExitsNonZero(t *testing.T) {
	jobTestEnv(t)
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := jobDone([]string{id, "--state", "wat"}); code == 0 {
		t.Fatal("job done with invalid state must exit non-zero")
	}
}

func TestJobDoneSecondTerminalExitsNonZero(t *testing.T) {
	jobTestEnv(t)
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := jobDone([]string{id, "--state", "succeeded"}); code != 0 {
		t.Fatalf("first done exit = %d, want 0", code)
	}
	if code := jobDone([]string{id, "--state", "failed"}); code == 0 {
		t.Fatal("second terminal done must exit non-zero")
	}
}

func TestJobDoneMissingJobArgExits2(t *testing.T) {
	jobTestEnv(t)
	if code := jobDone([]string{"--state", "succeeded"}); code != 2 {
		t.Fatalf("job done with no job id exit = %d, want 2", code)
	}
}

func TestJobProgressIsJournalOnly(t *testing.T) {
	jobTestEnv(t)
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "s"}) })
	id := trimTrailingNewline(out)
	if code := jobProgress([]string{id, "--message", "halfway"}); code != 0 {
		t.Fatalf("job progress exit = %d, want 0", code)
	}
	recs, _ := jobwake.ReadJob(jobwake.ChannelID("repo/branch"), id)
	if len(recs) != 2 || recs[1].Type != jobwake.TypeProgress || recs[1].Message != "halfway" {
		t.Fatalf("bad progress record: %+v", recs)
	}
}

func TestJobReadJobDetailEmitsFullStream(t *testing.T) {
	jobTestEnv(t)
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "moxy", "--label", "build"}) })
	id := trimTrailingNewline(out)
	_ = jobProgress([]string{id, "--message", "halfway"})
	_ = jobDone([]string{id, "--state", "succeeded", "--message", "done"})

	got := captureStdout(t, func() int { return jobRead([]string{"--job", id}) })
	// three records, one human line each, all naming the job id
	lines := nonEmptyLines(got)
	if len(lines) != 3 {
		t.Fatalf("want 3 lines for the job stream, got %d: %q", len(lines), got)
	}
}

func TestJobReadJSONDetail(t *testing.T) {
	jobTestEnv(t)
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "moxy"}) })
	id := trimTrailingNewline(out)
	_ = jobDone([]string{id, "--state", "succeeded"})
	got := captureStdout(t, func() int { return jobRead([]string{"--job", id, "--json"}) })
	lines := nonEmptyLines(got)
	if len(lines) != 2 {
		t.Fatalf("want 2 NDJSON lines, got %d: %q", len(lines), got)
	}
	for _, ln := range lines {
		if ln[0] != '{' {
			t.Fatalf("expected JSON object per line, got %q", ln)
		}
	}
}

func TestJobReadChannelWakingFilter(t *testing.T) {
	jobTestEnv(t)
	// one finished job, one still-running job
	out := captureStdout(t, func() int { return jobStart([]string{"--source", "moxy", "--label", "done-job"}) })
	doneID := trimTrailingNewline(out)
	_ = jobDone([]string{doneID, "--state", "succeeded"})
	out = captureStdout(t, func() int { return jobStart([]string{"--source", "moxy", "--label", "running-job"}) })
	_ = trimTrailingNewline(out)

	got := captureStdout(t, func() int { return jobRead(nil) })
	lines := nonEmptyLines(got)
	if len(lines) != 1 {
		t.Fatalf("channel read must return only the one waking event, got %d: %q", len(lines), got)
	}
}

func TestJobDisabledIsNoOp(t *testing.T) {
	jobTestEnv(t)
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	if code := runJob([]string{"start", "--source", "s"}); code != 0 {
		t.Fatalf("disabled job start exit = %d, want 0", code)
	}
	// Nothing should have been written.
	entries, err := os.ReadDir(jobwake.JournalDir(jobwake.ChannelID("repo/branch")))
	if err == nil && len(entries) > 0 {
		t.Fatalf("disabled job start must not write journal, found %d entries", len(entries))
	}
}

func TestJobWatchDisabledExitsZeroImmediately(t *testing.T) {
	jobTestEnv(t)
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	if code := runJobWatch(nil); code != 0 {
		t.Fatalf("disabled job-watch exit = %d, want 0", code)
	}
}

func TestProviderUsesPluginDirs(t *testing.T) {
	uses := map[string]bool{
		"claude":   true,
		"clownbox": true,
		"codex":    false,
		"circus":   false,
		"opencode": false,
		"crush":    false,
	}
	for provider, want := range uses {
		if got := providerUsesPluginDirs(provider); got != want {
			t.Errorf("providerUsesPluginDirs(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestRunJobUnknownSubcommandExits2(t *testing.T) {
	jobTestEnv(t)
	if code := runJob([]string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if code := runJob(nil); code != 2 {
		t.Fatalf("missing subcommand exit = %d, want 2", code)
	}
}

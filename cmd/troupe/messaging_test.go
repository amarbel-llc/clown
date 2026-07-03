package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// captureStdout swaps os.Stdout for a pipe, runs fn, and returns everything fn
// wrote. MUST NOT be used with t.Parallel() (mutates the process-global stdout).
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

func trimTrailingNewline(s string) string {
	return strings.TrimRight(s, "\n")
}

// troupeEnv isolates the journal + a short runtime dir per test (AF_UNIX
// sun_path is ~108 bytes, so the runtime dir must be short for nudges to bind).
func troupeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, err := os.MkdirTemp("/tmp", "clown-troupetest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("CLOWN_SESSION_ID", "repo/branch")
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")
}

// `troupe send --message` takes a git-commit-style message and splits it into
// the wake subject and the spool body. A self-directed send lands on the
// sender's own channel, so ReadChat recovers both halves.
func TestTroupeSendMessageSplitsGitCommit(t *testing.T) {
	troupeEnv(t) // CLOWN_SESSION_ID = "repo/branch"
	code := troupeSend([]string{"--target", "repo/branch",
		"--message", "the summary\n\nbody one\nbody two"})
	if code != 0 {
		t.Fatalf("send --message exit = %d, want 0", code)
	}
	msgs, err := jobwake.ReadChat(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("ReadChat returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Subject != "the summary" {
		t.Errorf("subject = %q, want the first line", msgs[0].Subject)
	}
	if msgs[0].Body != "body one\nbody two" {
		t.Errorf("body = %q, want the post-blank-line remainder", msgs[0].Body)
	}
}

func TestTroupeSendMessageRejectsMissingBlankLine(t *testing.T) {
	troupeEnv(t)
	code := troupeSend([]string{"--target", "repo/branch",
		"--message", "summary\nbody with no separator"})
	if code != 2 {
		t.Fatalf("send with no blank separator exit = %d, want 2", code)
	}
}

func TestTroupeSendMessageMutuallyExclusiveWithSubject(t *testing.T) {
	troupeEnv(t)
	code := troupeSend([]string{"--target", "repo/branch",
		"--message", "summary", "--subject", "also a subject"})
	if code != 2 {
		t.Fatalf("send --message + --subject exit = %d, want 2", code)
	}
}

func TestTroupeSendRequiresMessageOrSubject(t *testing.T) {
	troupeEnv(t)
	code := troupeSend([]string{"--target", "repo/branch"})
	if code != 2 {
		t.Fatalf("send with neither --message nor --subject exit = %d, want 2", code)
	}
}

// The explicit --subject/--body pair still works (operators/scripts).
func TestTroupeSendSubjectBodyStillWorks(t *testing.T) {
	troupeEnv(t)
	code := troupeSend([]string{"--target", "repo/branch",
		"--subject", "explicit subject", "--body", "explicit body"})
	if code != 0 {
		t.Fatalf("send --subject/--body exit = %d, want 0", code)
	}
	msgs, err := jobwake.ReadChat(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "explicit subject" || msgs[0].Body != "explicit body" {
		t.Fatalf("ReadChat = %+v, want explicit subject/body", msgs)
	}
}

// message prints the msg- job id and writes the single-record standalone waking
// job into the target channel with `from` carried.
func TestTroupeMessagePrintsIDAndWritesSingleRecord(t *testing.T) {
	troupeEnv(t)
	out := captureStdout(t, func() {
		troupeMessage([]string{"--target", "other-session", "--from", "repo/branch",
			"--source", "spinclass", "--message", "ping"})
	})
	id := trimTrailingNewline(out)
	if !strings.HasPrefix(id, "msg-") {
		t.Fatalf("message must print a msg- id, got %q", id)
	}
	recs, err := jobwake.ReadJob(jobwake.ChannelID("other-session"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Type != jobwake.TypeMessage || recs[0].Seq != 0 ||
		recs[0].From != "repo/branch" || recs[0].Message != "ping" {
		t.Fatalf("want one message record with from, got %+v", recs)
	}
}

func TestTroupeMessageBroadcastTarget(t *testing.T) {
	troupeEnv(t)
	out := captureStdout(t, func() {
		troupeMessage([]string{"--target", "*", "--from", "repo/branch",
			"--source", "spinclass", "--message", "to everyone"})
	})
	id := trimTrailingNewline(out)
	recs, err := jobwake.ReadJob(jobwake.ChannelID("*"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Session != "*" {
		t.Fatalf("broadcast message must land in the broadcast channel, got %+v", recs)
	}
}

func TestTroupeMessageMissingTargetExits2(t *testing.T) {
	troupeEnv(t)
	if code := troupeMessage([]string{"--message", "hi"}); code != 2 {
		t.Fatalf("message with no --target exit = %d, want 2", code)
	}
}

func TestTroupeMessageMissingMessageExits2(t *testing.T) {
	troupeEnv(t)
	if code := troupeMessage([]string{"--target", "k"}); code != 2 {
		t.Fatalf("message with no --message exit = %d, want 2", code)
	}
	if code := troupeMessage([]string{"--target", "k", "--message", ""}); code != 2 {
		t.Fatalf("message with empty --message exit = %d, want 2", code)
	}
}

// The dispatch kill-switch covers `message` like the other emits (RFC-0009 §8).
func TestTroupeMessageDisabledIsNoOp(t *testing.T) {
	troupeEnv(t)
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "1")
	if code := run([]string{"message", "--target", "k", "--message", "hi"}); code != 0 {
		t.Fatalf("disabled message exit = %d, want 0", code)
	}
	if entries, err := os.ReadDir(jobwake.JournalDir(jobwake.ChannelID("k"))); err == nil && len(entries) > 0 {
		t.Fatalf("disabled message must not write journal, found %d entries", len(entries))
	}
}

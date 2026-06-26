package main

import (
	"testing"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// `clown chat send --message` takes a git-commit-style message and splits it
// into the wake subject and the spool body. A self-directed send
// lands on the sender's own channel, so ReadChat recovers both halves.
func TestChatSendMessageSplitsGitCommit(t *testing.T) {
	jobTestEnv(t) // CLOWN_SESSION_ID = "repo/branch"
	code := chatSend([]string{"--target", "repo/branch",
		"--message", "the summary\n\nbody one\nbody two"})
	if code != 0 {
		t.Fatalf("chat send --message exit = %d, want 0", code)
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

func TestChatSendMessageRejectsMissingBlankLine(t *testing.T) {
	jobTestEnv(t)
	code := chatSend([]string{"--target", "repo/branch",
		"--message", "summary\nbody with no separator"})
	if code != 2 {
		t.Fatalf("chat send with no blank separator exit = %d, want 2", code)
	}
}

func TestChatSendMessageMutuallyExclusiveWithSubject(t *testing.T) {
	jobTestEnv(t)
	code := chatSend([]string{"--target", "repo/branch",
		"--message", "summary", "--subject", "also a subject"})
	if code != 2 {
		t.Fatalf("chat send --message + --subject exit = %d, want 2", code)
	}
}

func TestChatSendRequiresMessageOrSubject(t *testing.T) {
	jobTestEnv(t)
	code := chatSend([]string{"--target", "repo/branch"})
	if code != 2 {
		t.Fatalf("chat send with neither --message nor --subject exit = %d, want 2", code)
	}
}

// The explicit --subject/--body pair still works (operators/scripts).
func TestChatSendSubjectBodyStillWorks(t *testing.T) {
	jobTestEnv(t)
	code := chatSend([]string{"--target", "repo/branch",
		"--subject", "explicit subject", "--body", "explicit body"})
	if code != 0 {
		t.Fatalf("chat send --subject/--body exit = %d, want 0", code)
	}
	msgs, err := jobwake.ReadChat(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "explicit subject" || msgs[0].Body != "explicit body" {
		t.Fatalf("ReadChat = %+v, want explicit subject/body", msgs)
	}
}

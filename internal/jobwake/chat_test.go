package jobwake

import (
	"os"
	"testing"
)

func chatEnv(t *testing.T, sessionKey, spinclass string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", sessionKey)
	t.Setenv("SPINCLASS_SESSION_ID", spinclass)
}

func TestSendChatWritesRecordAndSpoolBody(t *testing.T) {
	chatEnv(t, "k", "")
	id, err := SendChat("k", "sender", "src", "the subject", "the\nfull\nbody")
	if err != nil {
		t.Fatal(err)
	}
	cid := ChannelID("k")
	recs, err := ReadJob(cid, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Type != TypeChat {
		t.Fatalf("want one chat record, got %+v", recs)
	}
	if recs[0].Message != "the subject" {
		t.Fatalf("record Message = %q, want the one-line subject", recs[0].Message)
	}
	b, err := os.ReadFile(SpoolFile(cid, id))
	if err != nil {
		t.Fatalf("spool body: %v", err)
	}
	if string(b) != "the\nfull\nbody" {
		t.Fatalf("spool body = %q, want the full multi-line body", string(b))
	}
}

func TestReadChatReturnsBodyAndAdvancesCursor(t *testing.T) {
	chatEnv(t, "k", "")
	if _, err := SendChat("k", "sender", "src", "hi", "body-1"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChat(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Subject != "hi" || got[0].Body != "body-1" || got[0].Scope != "direct" {
		t.Fatalf("want one direct chat with body, got %+v", got)
	}
	if again, err := ReadChat(false); err != nil {
		t.Fatal(err)
	} else if len(again) != 0 {
		t.Fatalf("cursor must advance; second read got %+v", again)
	}
}

func TestReadChatPeekDoesNotAdvance(t *testing.T) {
	chatEnv(t, "k", "")
	if _, err := SendChat("k", "sender", "src", "hi", "body"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadChat(true); err != nil || len(got) != 1 {
		t.Fatalf("peek read: got %+v err %v", got, err)
	}
	if again, err := ReadChat(true); err != nil || len(again) != 1 {
		t.Fatalf("peek must not advance; second peek got %+v err %v", again, err)
	}
}

// The load-bearing interaction: a direct chat must survive the monitor's
// own-channel reap (which targets TypeMessage), so chat-read still finds the
// body after a monitor cycle (the chat cursor is distinct from the wake ack).
func TestChatSurvivesMonitorReap(t *testing.T) {
	chatEnv(t, "k", "")
	id, err := SendChat("k", "sender", "src", "hi", "body")
	if err != nil {
		t.Fatal(err)
	}
	// The monitor delivers (and would reap a delivered TypeMessage here).
	if err := ReplayOnce("k", func(Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if recs, err := ReadJob(ChannelID("k"), id); err != nil || len(recs) != 1 {
		t.Fatalf("chat record must survive the reap; got %+v err %v", recs, err)
	}
	got, err := ReadChat(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "body" {
		t.Fatalf("chat-read must still see the body after a monitor cycle; got %+v", got)
	}
}

// A chat addressed to SPINCLASS_SESSION_ID is read via the reader's group channel.
func TestReadChatGroupScope(t *testing.T) {
	chatEnv(t, "instance-1", "repo/branch")
	if _, err := SendChat("repo/branch", "sibling", "src", "team", "group-body"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChat(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Scope != "group" || got[0].Body != "group-body" {
		t.Fatalf("want one group chat with body, got %+v", got)
	}
}

// chat-read INCLUDES the reader's own sent messages (conversation history),
// unlike the wake monitor which suppresses self-echo.
func TestReadChatIncludesOwnSent(t *testing.T) {
	chatEnv(t, "me", "")
	if _, err := SendChat("me", "me", "src", "note", "to-self"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChat(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != "me" || got[0].Body != "to-self" {
		t.Fatalf("chat-read must include own sent messages, got %+v", got)
	}
}

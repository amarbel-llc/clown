package jobwake

import (
	"path/filepath"
	"testing"
)

func TestSessionKeyResolutionOrder(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
	if got := SessionKey(); got != "repo/branch" {
		t.Fatalf("want spinclass key, got %q", got)
	}
	t.Setenv("CLOWN_SESSION_ID", "explicit")
	if got := SessionKey(); got != "explicit" {
		t.Fatalf("CLOWN_SESSION_ID must win, got %q", got)
	}
}

func TestSessionKeyGeneratedWhenUnset(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	got := SessionKey()
	if len(got) != 32 {
		t.Fatalf("generated key must be 32 hex chars, got %q (len %d)", got, len(got))
	}
}

func TestChannelIDStableAnd32Hex(t *testing.T) {
	a := ChannelID("repo/branch")
	if len(a) != 32 {
		t.Fatalf("channel id must be 32 hex chars, got %q", a)
	}
	if a != ChannelID("repo/branch") {
		t.Fatal("channel id must be deterministic")
	}
	if a == ChannelID("repo/other") {
		t.Fatal("distinct keys must yield distinct channel ids")
	}
}

func TestJournalPathsUnderStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xsh")
	cid := ChannelID("k")
	want := filepath.Join("/tmp/xsh", "clown", "jobs", cid, "job1.jsonl")
	if got := JournalFile(cid, "job1"); got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

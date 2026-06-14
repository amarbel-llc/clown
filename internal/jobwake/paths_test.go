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

// ResolveSessionKey reports both the key and which precedence branch supplied
// it — the source label that backs `clown job whoami` (clown#135).
func TestResolveSessionKeySource(t *testing.T) {
	cases := []struct {
		name                string
		clown, spin, claude string
		wantKey, wantSource string
	}{
		{"clown wins", "clown-key", "repo/branch", "claude-x", "clown-key", "CLOWN_SESSION_ID"},
		{"spinclass when no clown", "", "repo/branch", "claude-x", "repo/branch", "SPINCLASS_SESSION_ID"},
		{"claude when no clown/spin", "", "", "claude-x", "claude-x", "CLAUDE_SESSION_ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLOWN_SESSION_ID", tc.clown)
			t.Setenv("SPINCLASS_SESSION_ID", tc.spin)
			t.Setenv("CLAUDE_SESSION_ID", tc.claude)
			k, s := ResolveSessionKey()
			if k != tc.wantKey || s != tc.wantSource {
				t.Fatalf("got (%q, %q), want (%q, %q)", k, s, tc.wantKey, tc.wantSource)
			}
		})
	}

	// All unset → a generated key with source "generated".
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	k, s := ResolveSessionKey()
	if s != "generated" {
		t.Fatalf("all unset: source = %q, want generated", s)
	}
	if len(k) != 32 {
		t.Fatalf("generated key = %q (len %d), want 32 hex", k, len(k))
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

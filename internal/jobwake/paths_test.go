package jobwake

import (
	"path/filepath"
	"strings"
	"testing"
)

func isUUIDShaped(s string) bool { return len(s) == 36 && strings.Count(s, "-") == 4 }

func TestSessionKeyResolutionOrder(t *testing.T) {
	// SPINCLASS_SESSION_ID is the group decoration, NOT the routing key
	// (RFC-0013 §2.3): with no CLOWN/CLAUDE id it must not supply the key.
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
	if got := SessionKey(); got == "repo/branch" {
		t.Fatalf("SPINCLASS_SESSION_ID must not supply the routing key, got %q", got)
	}
	t.Setenv("CLAUDE_SESSION_ID", "claude-x")
	if got := SessionKey(); got != "claude-x" {
		t.Fatalf("want CLAUDE_SESSION_ID when CLOWN unset, got %q", got)
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
	if !isUUIDShaped(got) {
		t.Fatalf("generated key must be a UUIDv4, got %q (len %d)", got, len(got))
	}
}

func TestGroupKeyAndGroupChannel(t *testing.T) {
	t.Setenv("SPINCLASS_SESSION_ID", "")
	if GroupKey() != "" || GroupChannel() != "" {
		t.Fatal("no spinclass decoration: GroupKey/GroupChannel must be empty")
	}
	t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
	if got := GroupKey(); got != "repo/branch" {
		t.Fatalf("GroupKey = %q, want repo/branch", got)
	}
	if got, want := GroupChannel(), ChannelID("repo/branch"); got != want {
		t.Fatalf("GroupChannel = %q, want ChannelID(repo/branch) = %q", got, want)
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
		{"claude when no clown; spinclass ignored", "", "repo/branch", "claude-x", "claude-x", "CLAUDE_SESSION_ID"},
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

	// SPINCLASS_SESSION_ID alone does NOT supply the key (RFC-0013 §2.3) — it
	// falls through to a generated UUID.
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
	t.Setenv("CLAUDE_SESSION_ID", "")
	if k, s := ResolveSessionKey(); s != "generated" || !isUUIDShaped(k) {
		t.Fatalf("spinclass-only: got (%q, %q), want a generated UUID", k, s)
	}

	// All unset → a generated UUIDv4 with source "generated".
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	k, s := ResolveSessionKey()
	if s != "generated" {
		t.Fatalf("all unset: source = %q, want generated", s)
	}
	if !isUUIDShaped(k) {
		t.Fatalf("generated key = %q (len %d), want a UUIDv4", k, len(k))
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

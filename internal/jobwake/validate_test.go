package jobwake

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateJobID pins the RFC-0009 §4 grammar plus the clown#123 `.`/`..`
// reject. The grammar already excludes "/", so the real traversal vector
// ("../foo") is a grammar failure; the explicit `.`/`..` reject covers the
// suffix-stripped forms the grammar admits.
func TestValidateJobID(t *testing.T) {
	valid := []string{"build-3f2ab1c9", "msg-abcd1234", "a", "A.b_c-1", "..jsonl"}
	for _, id := range valid {
		if err := validateJobID(id); err != nil {
			t.Errorf("validateJobID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{"", ".", "..", "../foo", "a/b", "/abs", "foo bar", "a\nb"}
	for _, id := range invalid {
		if err := validateJobID(id); err == nil {
			t.Errorf("validateJobID(%q) = nil, want error", id)
		}
	}
	// Grammar upper bound: 128 ok, 129 rejected.
	if err := validateJobID(strings.Repeat("a", 128)); err != nil {
		t.Errorf("128-char id rejected: %v", err)
	}
	if err := validateJobID(strings.Repeat("a", 129)); err == nil {
		t.Error("129-char id accepted, want error")
	}
}

// TestDoneRejectsTraversalJobID is the clown#123 reproduction: a consume-path
// call with a traversal id must error before composing a path, and must NOT
// write a journal outside the channel directory.
func TestDoneRejectsTraversalJobID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	if err := Done("", "../../escape", TypeSucceeded, "", ""); err == nil {
		t.Fatal("Done with traversal job id must error")
	}
	// Two levels up from the channel dir (<state>/clown/jobs/<cid>) is
	// <state>/clown; the would-be escape write lands there.
	escaped := filepath.Join(JournalDir(ChannelID("k")), "..", "..", "escape.jsonl")
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("traversal write escaped the channel dir: %v at %s", err, escaped)
	}
}

func TestProgressRejectsTraversalJobID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	if err := Progress("", "../evil", "x"); err == nil {
		t.Fatal("Progress with traversal job id must error")
	}
}

func TestReadJobRejectsTraversalJobID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := ReadJob(ChannelID("k"), "../../etc/passwd"); err == nil {
		t.Fatal("ReadJob with traversal job id must error")
	}
}

package sessions

import "testing"

func TestKeySpinclassWorktreeShape(t *testing.T) {
	// The spinclass layout is derivable from the path alone — even when the
	// directory no longer exists (deleted worktree) — and the worktree
	// segment beats the recorded branch.
	s := Session{
		CWD:       "/home/u/eng/repos/doppelgang/.worktrees/quiet-ebony",
		GitBranch: "quiet-ebony",
	}
	if got := s.Key(); got != "doppelgang/quiet-ebony" {
		t.Errorf("Key() = %q, want doppelgang/quiet-ebony", got)
	}
}

func TestKeyWorktreeSegmentBeatsBranch(t *testing.T) {
	// A recorded branch that diverges from the worktree name (rebases,
	// renames) must not change the session's key: the worktree IS the
	// spinclass identity.
	s := Session{
		CWD:       "/home/u/eng/repos/moxy/.worktrees/loud-elder",
		GitBranch: "some-feature-branch",
	}
	if got := s.Key(); got != "moxy/loud-elder" {
		t.Errorf("Key() = %q, want moxy/loud-elder", got)
	}
}

func TestKeyDeadMainCheckoutFallsBackToBasename(t *testing.T) {
	s := Session{
		CWD:       "/home/u/somewhere/gone/myrepo",
		GitBranch: "master",
	}
	if got := s.Key(); got != "myrepo/master" {
		t.Errorf("Key() = %q, want myrepo/master", got)
	}
}

func TestKeyNoBranchDegradesToRepo(t *testing.T) {
	s := Session{CWD: "/home/u/gone/myrepo"}
	if got := s.Key(); got != "myrepo" {
		t.Errorf("Key() = %q, want myrepo", got)
	}
}

func TestKeyEmptyCWDUnmatchable(t *testing.T) {
	s := Session{GitBranch: "master"}
	if got := s.Key(); got != "" {
		t.Errorf("Key() = %q, want \"\"", got)
	}
}

func TestFilterByKeyPreservesOrderAndScope(t *testing.T) {
	a := Session{ID: "a", CWD: "/x/repos/r/.worktrees/w1", GitBranch: "w1"}
	b := Session{ID: "b", CWD: "/x/repos/r/.worktrees/w2", GitBranch: "w2"}
	c := Session{ID: "c", CWD: "/x/repos/r/.worktrees/w1", GitBranch: "w1"}

	got := FilterByKey([]Session{a, b, c}, "r/w1")
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("FilterByKey = %+v, want [a c]", got)
	}
	if got := FilterByKey([]Session{a, b}, ""); got != nil {
		t.Fatalf("empty key must match nothing, got %+v", got)
	}
}

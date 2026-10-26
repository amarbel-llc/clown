package main

import (
	"errors"
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/sessions"
)

const spawnBriefTestBrief = "Implement the thing, then message me back."

func spawnFlags() parsedFlags {
	f := parsedFlags{provider: "claude", profile: "worker", forwarded: []string{"--permission-mode", "auto", "--model", "opus", spawnBriefTestBrief}}
	f.identity.Key = restartTestPrincipal
	return f
}

func withSpawnBriefLookup(t *testing.T, fn func(string) (string, bool, error)) {
	t.Helper()
	old := spawnBriefDigestOf
	spawnBriefDigestOf = fn
	t.Cleanup(func() { spawnBriefDigestOf = old })
}

// The relaunch resumes the conversation without sending the spawn brief into
// it again: exactly the recorded operand is left out, the flags stay.
func TestRestartArgvLeavesOutTheRecordedBrief(t *testing.T) {
	withSpawnBriefLookup(t, func(id string) (string, bool, error) {
		if id != restartTestPrincipal {
			t.Errorf("looked up %q, want the principal", id)
		}
		return sessions.BriefDigest(spawnBriefTestBrief), true, nil
	})
	got := restartArgv(spawnFlags(), "")
	want := []string{"--provider", "claude", "--profile", "worker", "--", "--permission-mode", "auto", "--model", "opus", "--resume", restartTestPrincipal}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// Nothing recorded, an unreadable sidecar, or a launch whose operands do not
// include the recorded brief (an already-stripped relaunch, a literal
// operand) keeps every operand: the clown#318 behaviour.
func TestRestartArgvKeepsOperandsWithoutAMatchingBrief(t *testing.T) {
	f := spawnFlags()
	// With no `--` in forwarded the new --resume lands after the operands.
	keep := []string{"--provider", "claude", "--profile", "worker", "--", "--permission-mode", "auto", "--model", "opus", spawnBriefTestBrief, "--resume", restartTestPrincipal}
	for _, tc := range []struct {
		name   string
		lookup func(string) (string, bool, error)
	}{
		{"none recorded", func(string) (string, bool, error) { return "", false, nil }},
		{"sidecar error", func(string) (string, bool, error) { return "", false, errors.New("boom") }},
		{"other digest", func(string) (string, bool, error) { return sessions.BriefDigest("something else"), true, nil }},
		{"only a prefix", func(string) (string, bool, error) { return sessions.BriefDigest(spawnBriefTestBrief[:10]), true, nil }},
	} {
		withSpawnBriefLookup(t, tc.lookup)
		if got := restartArgv(f, ""); !slices.Equal(got, keep) {
			t.Errorf("%s: argv = %q, want %q", tc.name, got, keep)
		}
	}
}

// A flag value that happens to equal the brief is not special: only the last
// matching operand goes.
func TestRestartArgvLeavesOutOnlyOneBrief(t *testing.T) {
	withSpawnBriefLookup(t, func(string) (string, bool, error) { return sessions.BriefDigest("x"), true, nil })
	f := parsedFlags{provider: "claude", forwarded: []string{"--append-system-prompt", "x", "x"}}
	f.identity.Key = restartTestPrincipal
	got := restartArgv(f, "")
	want := []string{"--provider", "claude", "--", "--append-system-prompt", "x", "--resume", restartTestPrincipal}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// A spawn launch records its brief (the last operand spinclass puts after the
// provider-args) by digest; any other launch records nothing.
func TestRecordSpawnBrief(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := spawnFlags()

	if err := recordSpawnBrief(f, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := sessions.SpawnBriefDigestOf(restartTestPrincipal); ok {
		t.Fatal("a non-spawn launch recorded a brief")
	}
	empty := f
	empty.forwarded = nil
	if err := recordSpawnBrief(empty, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := sessions.SpawnBriefDigestOf(restartTestPrincipal); ok {
		t.Fatal("a spawn with no operands recorded a brief")
	}
	// A spawn that carried no brief ends in a flag or an empty operand: nothing
	// is recorded, or a restart would drop a real flag.
	for _, tail := range []string{"--dangerously-skip-permissions", "-p", ""} {
		bare := f
		bare.forwarded = []string{"--model", "opus", tail}
		if err := recordSpawnBrief(bare, true); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := sessions.SpawnBriefDigestOf(restartTestPrincipal); ok {
			t.Fatalf("a spawn ending in %q recorded a brief", tail)
		}
	}
	if err := recordSpawnBrief(f, true); err != nil {
		t.Fatal(err)
	}
	got, ok, err := sessions.SpawnBriefDigestOf(restartTestPrincipal)
	if err != nil || !ok || got != sessions.BriefDigest(spawnBriefTestBrief) {
		t.Fatalf("recorded = %q, %v, %v; want the digest of the brief", got, ok, err)
	}
}

// A markdown brief that starts with a dash is still a brief; a bare dash
// token is a flag. Neither a recorded flag digest (a stale or planted record)
// nor an empty operand makes the relaunch drop a flag.
func TestBriefLookingTokens(t *testing.T) {
	for tok, want := range map[string]bool{
		"- fix the thing\n- and the other": true,
		spawnBriefTestBrief:                true,
		"--model":                          false,
		"-p":                               false,
		"--dangerously-skip-permissions":   false,
		"":                                 false,
	} {
		if got := looksLikeBrief(tok); got != want {
			t.Errorf("looksLikeBrief(%q) = %v, want %v", tok, got, want)
		}
	}
	withSpawnBriefLookup(t, func(string) (string, bool, error) { return sessions.BriefDigest("--verbose"), true, nil })
	f := parsedFlags{provider: "claude", forwarded: []string{"--verbose", "--model", "opus"}}
	f.identity.Key = restartTestPrincipal
	want := []string{"--provider", "claude", "--", "--verbose", "--model", "opus", "--resume", restartTestPrincipal}
	if got := restartArgv(f, ""); !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want a flag kept even when its digest was recorded: %q", got, want)
	}
}

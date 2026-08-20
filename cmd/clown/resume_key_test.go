package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"

	"code.linenisgreat.com/clown/internal/sessions"
)

// Key detection: a slashed positional that is not a URI parses into
// key (first two segments) + optional keyName (third segment).
func TestParseResumeArgs_KeyPositional(t *testing.T) {
	cases := []struct {
		name        string
		arg         string
		wantKey     string
		wantKeyName string
	}{
		{"two segments", "clown/feature-x", "clown/feature-x", ""},
		{"three segments", "clown/feature-x/bozo", "clown/feature-x", "bozo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResumeArgs([]string{tc.arg})
			if err != nil {
				t.Fatalf("parseResumeArgs(%q): %v", tc.arg, err)
			}
			if got.key != tc.wantKey {
				t.Errorf("key = %q, want %q", got.key, tc.wantKey)
			}
			if got.keyName != tc.wantKeyName {
				t.Errorf("keyName = %q, want %q", got.keyName, tc.wantKeyName)
			}
			if got.uri != "" {
				t.Errorf("uri = %q, want empty for a key positional", got.uri)
			}
		})
	}
}

// Non-key positionals stay in the uri lane: URIs (contain ://) and bare
// words without a slash.
func TestParseResumeArgs_NonKeyPositionalsStayURI(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"uri is not a key", "clown://claude/abc-123"},
		{"bare word is not a key", "abc-123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResumeArgs([]string{tc.arg})
			if err != nil {
				t.Fatalf("parseResumeArgs(%q): %v", tc.arg, err)
			}
			if got.key != "" || got.keyName != "" {
				t.Errorf("key/keyName = %q/%q, want empty", got.key, got.keyName)
			}
			if got.uri != tc.arg {
				t.Errorf("uri = %q, want %q", got.uri, tc.arg)
			}
		})
	}
}

// Key and URI are mutually exclusive positionals, in either order, and two
// keys are rejected too.
func TestParseResumeArgs_RejectsSecondPositionalWithKey(t *testing.T) {
	cases := [][]string{
		{"repo/wt", "clown://claude/abc"},
		{"clown://claude/abc", "repo/wt"},
		{"repo/wt", "other/wt"},
	}
	for i, args := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			if _, err := parseResumeArgs(args); err == nil {
				t.Errorf("expected error for two positionals, got nil (args=%v)", args)
			}
		})
	}
}

// Key mode rejects --provider (a key implies claude) but, unlike the URI
// form, allows -y/--yes: a key is a scope, not a specific conversation.
func TestParseResumeArgs_KeyValidation(t *testing.T) {
	rejected := [][]string{
		{"repo/wt", "--provider", "codex"},
		{"--provider=codex", "repo/wt"},
	}
	for i, args := range rejected {
		t.Run(fmt.Sprintf("provider-rejected-%d", i), func(t *testing.T) {
			if _, err := parseResumeArgs(args); err == nil {
				t.Errorf("expected error for key + --provider, got nil (args=%v)", args)
			}
		})
	}

	got, err := parseResumeArgs([]string{"repo/wt", "-y"})
	if err != nil {
		t.Fatalf("key + -y must be accepted: %v", err)
	}
	if got.key != "repo/wt" || !got.yes {
		t.Errorf("key=%q yes=%v, want repo/wt true", got.key, got.yes)
	}
}

func TestPoshAttachHint(t *testing.T) {
	p := jobwake.Presence{SessionKey: "inst-a"}
	if got := poshAttachHint(p); got != "posh attach inst-a" {
		t.Errorf("poshAttachHint = %q, want %q", got, "posh attach inst-a")
	}
}

// A live presence record whose group matches the key wins: resumeByKey
// prints the attach hint and exits 0 without touching dead sessions.
func TestResumeByKey_LiveSessionPrintsAttachHint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixtureNamed(t, "inst-a", "repo/feature", "desc", "bozo")

	var code int
	out := captureStderr(t, func() int {
		code = resumeByKey(resumeArgs{provider: "claude", key: "repo/feature"})
		return code
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "LIVE") || !strings.Contains(out, "bozo") || !strings.Contains(out, "posh attach inst-a") {
		t.Fatalf("live hint missing expected fields: %q", out)
	}
}

// With a clown-name segment, only live records whose ClownName matches
// count.
func TestResumeByKey_LiveNamedMatch(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixtureNamed(t, "inst-a", "repo/feature", "desc", "bozo")
	registerPresenceFixtureNamed(t, "inst-b", "repo/feature", "desc", "krusty")

	var code int
	out := captureStderr(t, func() int {
		code = resumeByKey(resumeArgs{provider: "claude", key: "repo/feature", keyName: "krusty"})
		return code
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "posh attach inst-b") {
		t.Fatalf("named live hint missing: %q", out)
	}
	if strings.Contains(out, "inst-a") {
		t.Fatalf("hint must exclude the other-named record: %q", out)
	}
}

// Live records exist for the key but none carry the requested name: say so
// and continue to dead resolution (which, with an empty HOME, finds
// nothing and exits 1).
func TestResumeByKey_LiveNameMismatchFallsThroughToDead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	registerPresenceFixtureNamed(t, "inst-a", "repo/feature", "desc", "bozo")

	var code int
	out := captureStderr(t, func() int {
		code = resumeByKey(resumeArgs{provider: "claude", key: "repo/feature", keyName: "krusty"})
		return code
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (no dead sessions in empty HOME)", code)
	}
	if !strings.Contains(out, `none named "krusty"`) {
		t.Fatalf("missing the name-mismatch note: %q", out)
	}
	if !strings.Contains(out, "no resumable claude sessions") {
		t.Fatalf("missing the dead-resolution miss: %q", out)
	}
}

// writeDeadSessionFixture fabricates one claude transcript under
// home/.claude/projects so ListClaudeSessions discovers a dead session
// with the given id, recorded cwd, and mtime.
func writeDeadSessionFixture(t *testing.T, home, id, cwd string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	line := fmt.Sprintf(`{"cwd":%q,"gitBranch":"w1"}`+"\n", cwd)
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// Dead-path name filter (clown#192 step 3): the third key segment selects
// the newest conversation whose SIDECAR-recorded name matches — here the
// OLDER session, which the unfiltered path would never pick. Reaching
// resumeSingle's non-tty guard (exit 1 with the interactive-terminal
// message) after the older session's gone-directory note proves the
// filter chose it.
func TestResumeByKey_DeadNameFilterSelectsRecordedName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeDeadSessionFixture(t, home, "id-old", "/x/repos/r/.worktrees/w1", time.Now().Add(-2*time.Hour))
	writeDeadSessionFixture(t, home, "id-new", "/y/repos/r/.worktrees/w1", time.Now().Add(-1*time.Hour))
	if err := sessions.RecordSessionName("id-old", "bozo", "r/w1"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.RecordSessionName("id-new", "krusty", "r/w1"); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStderr(t, func() int {
		code = resumeByKey(resumeArgs{provider: "claude", key: "r/w1", keyName: "bozo"})
		return code
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (non-tty confirm guard)", code)
	}
	if !strings.Contains(out, "resume requires an interactive terminal") {
		t.Fatalf("expected to reach resumeSingle's non-tty guard: %q", out)
	}
	if !strings.Contains(out, `"/x/repos/r/.worktrees/w1"`) {
		t.Fatalf("expected the OLDER (bozo) session's gone-directory note: %q", out)
	}
}

// The core "resume where it lived" behavior: when the recorded cwd still
// exists on disk, resumeByKey actually chdirs into it before falling through
// to resumeSingle. Every other dead-path test above uses a cwd that doesn't
// exist, exercising only the "directory is gone" branch — this covers the
// chdir-succeeds branch itself.
func TestResumeByKey_ChdirsIntoExistingRecordedDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)

	base := t.TempDir()
	recorded := filepath.Join(base, "repos", "r", ".worktrees", "w1")
	if err := os.MkdirAll(recorded, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDeadSessionFixture(t, home, "id-1", recorded, time.Now())

	// Restore-only guard: resumeByKey chdirs as a side effect (asserted
	// below), and t.Chdir registers the restoration for later tests.
	t.Chdir(".")

	var code int
	out := captureStderr(t, func() int {
		code = resumeByKey(resumeArgs{provider: "claude", key: "r/w1"})
		return code
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (non-tty confirm guard)", code)
	}
	if strings.Contains(out, "is gone") {
		t.Fatalf("recorded dir exists; must not report it as gone: %q", out)
	}
	gotCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !sessions.SameDir(gotCWD, recorded, "") {
		t.Fatalf("cwd = %q, want to have chdir'd into %q", gotCWD, recorded)
	}
}

// A name segment that matches no recorded conversation is a clear miss.
func TestResumeByKey_DeadNameFilterMiss(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeDeadSessionFixture(t, home, "id-1", "/x/repos/r/.worktrees/w1", time.Now().Add(-time.Hour))
	if err := sessions.RecordSessionName("id-1", "bozo", "r/w1"); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStderr(t, func() int {
		code = resumeByKey(resumeArgs{provider: "claude", key: "r/w1", keyName: "grock"})
		return code
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, `no resumable claude sessions for key "r/w1" named "grock"`) {
		t.Fatalf("missing the named-miss message: %q", out)
	}
}

// Nothing live and nothing recorded: a clear miss, exit 1.
func TestResumeByKey_NoMatchesAnywhere(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	var code int
	out := captureStderr(t, func() int {
		code = resumeByKey(resumeArgs{provider: "claude", key: "repo/absent"})
		return code
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, `no resumable claude sessions for key "repo/absent"`) {
		t.Fatalf("missing the miss message: %q", out)
	}
}

package main

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// registerPresenceFixture writes one presence record with the given key, group,
// and description by driving jobwake.RegisterPresence through the env it reads.
// XDG_STATE_HOME must already point at a temp dir.
func registerPresenceFixture(t *testing.T, key, group, desc string) {
	t.Helper()
	registerPresenceFixtureNamed(t, key, group, desc, "")
}

// registerPresenceFixtureNamed is registerPresenceFixture plus CLOWN_NAME
// (clown#169/clown#179) — split out so the many existing callers that don't
// care about the name stay unchanged.
func registerPresenceFixtureNamed(t *testing.T, key, group, desc, clownName string) {
	t.Helper()
	t.Setenv("CLOWN_SESSION_ID", key)
	t.Setenv("CLOWN_GROUP_ID", group)
	t.Setenv("CLOWN_GROUP_DESCRIPTION", desc)
	t.Setenv("CLOWN_NAME", clownName)
	if err := jobwake.RegisterPresence(time.Now()); err != nil {
		t.Fatal(err)
	}
}

// --quiet turns the command into a liveness predicate: exit 0 when a matching
// live record exists, exit 1 when none. This is spinclass's probe form.
func TestPresenceListQuietPredicate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixture(t, "inst-a", "repo/feature", "A")
	registerPresenceFixture(t, "inst-b", "repo/other", "B")

	if code := presenceList([]string{"--group", "repo/feature", "--quiet"}); code != 0 {
		t.Fatalf("quiet match exit = %d, want 0", code)
	}
	if code := presenceList([]string{"--group", "repo/missing", "--quiet"}); code != 1 {
		t.Fatalf("quiet miss exit = %d, want 1", code)
	}
}

// --group filters by decoration; --json emits the matching records only.
func TestPresenceListJSONFiltersByGroup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixture(t, "inst-a", "repo/feature", "A")
	registerPresenceFixture(t, "inst-b", "repo/other", "B")

	out := captureStdout(t, func() int {
		return presenceList([]string{"--group", "repo/feature", "--json"})
	})
	if !strings.Contains(out, `"sessionKey":"inst-a"`) {
		t.Fatalf("json missing the matching record: %q", out)
	}
	if strings.Contains(out, "inst-b") {
		t.Fatalf("json must exclude the other group: %q", out)
	}
}

// An explicit empty --group selects the ungrouped records (decoration == ""),
// distinct from omitting --group (which lists everything).
func TestPresenceListEmptyGroupSelectsUngrouped(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixture(t, "inst-bare", "", "ungrouped")
	registerPresenceFixture(t, "inst-grouped", "repo/feature", "grouped")

	out := captureStdout(t, func() int {
		return presenceList([]string{"--group", "", "--json"})
	})
	if !strings.Contains(out, `"sessionKey":"inst-bare"`) {
		t.Fatalf("empty --group must select the ungrouped record: %q", out)
	}
	if strings.Contains(out, "inst-grouped") {
		t.Fatalf("empty --group must exclude grouped records: %q", out)
	}

	// Omitting --group lists every group.
	all := captureStdout(t, func() int { return presenceList([]string{"--json"}) })
	if !strings.Contains(all, "inst-bare") || !strings.Contains(all, "inst-grouped") {
		t.Fatalf("no --group must list all records: %q", all)
	}
}

// The human listing (no --json/--quiet) shows "<name> (<sessionKey>)" when a
// record carries a ClownName (clown#169/clown#179), and falls back to the
// bare sessionKey — exactly the pre-clown#169 output — when it doesn't, so
// an older ringmaster build or a pre-allocator session degrades gracefully.
func TestPresenceListHumanListingShowsClownName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixtureNamed(t, "inst-named", "repo/feature", "has a name", "bozo")
	registerPresenceFixture(t, "inst-bare", "repo/feature", "no name yet")

	out := captureStdout(t, func() int { return presenceList(nil) })
	if !strings.Contains(out, "bozo (inst-named)") {
		t.Fatalf("human listing missing clown-name-prefixed row: %q", out)
	}
	if !strings.Contains(out, "  inst-bare  ") {
		t.Fatalf("human listing missing bare-sessionKey fallback row: %q", out)
	}
}

func TestRunPresenceUnknownSubcommand(t *testing.T) {
	if code := runPresence([]string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if code := runPresence(nil); code != 2 {
		t.Fatalf("no subcommand exit = %d, want 2", code)
	}
}

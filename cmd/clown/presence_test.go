package main

import (
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/ringmaster/jobwake"
)

// registerPresenceFixture writes one presence record with the given key, group,
// and description by driving jobwake.RegisterPresence through the env it reads.
// XDG_STATE_HOME must already point at a temp dir.
func registerPresenceFixture(t *testing.T, key, group, desc string) {
	t.Helper()
	t.Setenv("CLOWN_SESSION_ID", key)
	t.Setenv("CLOWN_GROUP_ID", group)
	t.Setenv("CLOWN_GROUP_DESCRIPTION", desc)
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

func TestRunPresenceUnknownSubcommand(t *testing.T) {
	if code := runPresence([]string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if code := runPresence(nil); code != 2 {
		t.Fatalf("no subcommand exit = %d, want 2", code)
	}
}

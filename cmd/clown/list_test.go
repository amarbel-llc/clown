package main

import (
	"os"
	"strings"
	"testing"
)

// clown list is a flat table (spinclass-list-shaped), distinct from clown
// presence list's grouped-by-decoration view, over the same presence data.
func TestListHumanTableShowsAllSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixtureNamed(t, "inst-a", "repo/feature", "in a group", "bozo")
	registerPresenceFixtureNamed(t, "inst-b", "", "no group", "")

	out := captureStdout(t, func() int { return runList(nil) })
	if !strings.Contains(out, "bozo") || !strings.Contains(out, "inst-a") || !strings.Contains(out, "repo/feature") {
		t.Fatalf("grouped row missing expected fields: %q", out)
	}
	// The ungrouped row's SPINCLASS/NAME columns fall back to "-", not empty
	// cells, so the table stays aligned/readable.
	var ungroupedLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "inst-b") {
			ungroupedLine = line
		}
	}
	if ungroupedLine == "" {
		t.Fatalf("ungrouped row missing: %q", out)
	}
	fields := strings.Fields(ungroupedLine)
	// NAME, SESSION, SPINCLASS, then the (possibly multi-word) DESCRIPTION.
	if len(fields) < 4 || fields[0] != "-" || fields[1] != "inst-b" || fields[2] != "-" {
		t.Fatalf("ungrouped row fields = %v, want [- inst-b - ...] (name/spinclass placeholders)", fields)
	}
}

// --json now emits mesa NDJSON (breaking change from raw jobwake.Presence JSON):
// a header record with column definitions, then one cells record per session.
func TestListJSONEmitsPresenceRecords(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	registerPresenceFixtureNamed(t, "inst-a", "repo/feature", "desc", "bozo")

	out := captureStdout(t, func() int { return runList([]string{"--json"}) })
	// Header record must declare the four columns.
	if !strings.Contains(out, `"columns"`) || !strings.Contains(out, `"NAME"`) {
		t.Fatalf("json missing column header: %q", out)
	}
	// Row record must contain the session key in the cells array.
	if !strings.Contains(out, `"cells"`) || !strings.Contains(out, `"inst-a"`) {
		t.Fatalf("json missing session row: %q", out)
	}
}

// list works with NO presence records at all (a fresh store) — an empty table,
// not an error.
func TestListEmptyIsNotAnError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	startJobService(t)
	if code := runList(nil); code != 0 {
		t.Fatalf("empty list exit = %d, want 0", code)
	}
}

// With no host service to ask (ringmaster FDR-0001: the store is private to it),
// list fails loudly rather than reporting an empty table that is not true.
func TestListWithoutTheServiceFailsLoudly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, err := os.MkdirTemp("", "rm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	if code := runList(nil); code != 1 {
		t.Fatalf("list with no service exit = %d, want 1", code)
	}
}

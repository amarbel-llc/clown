package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// juggler's own copies of clown's internal/testlane.SkipAllowed and
// SkipOrFail: juggler imports nothing from clown outside the juggler tree
// (clown#258, `just lint-juggler-boundary`).

// skipTB is the part of testing.TB that skipAllowed and skipOrFail use.
type skipTB interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// skipAllowed skips t in and out of the nix test lane: an allowlisted skip
// for a test the lane deliberately does not run (clown#235). reason is
// required (a blank one fails the test) and is logged with the skip, so
// every allowlisted skip is greppable (skipAllowed) and says why.
func skipAllowed(t skipTB, reason string) {
	t.Helper()
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("skipAllowed needs a reason")
		return
	}
	t.Skipf("allowlisted skip: %s", reason)
}

// skipOrFail skips t outside the nix test lane and fails it inside, where
// every prerequisite is provided: a missing one there is a lane bug, not a
// reason to leave the test unrun (clown#235, clown#311).
func skipOrFail(t skipTB, reason string) {
	t.Helper()
	if os.Getenv("CLOWN_NIX_TEST_LANE") == "1" {
		t.Fatalf("prerequisite missing in the nix test lane: %s", reason)
		return
	}
	t.Skipf("%s", reason)
}

type skipRecorder struct {
	skipped, failed string
}

func (r *skipRecorder) Helper() {}

func (r *skipRecorder) Skipf(format string, args ...any) {
	r.skipped = fmt.Sprintf(format, args...)
}

func (r *skipRecorder) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
}

func TestSkipAllowed(t *testing.T) {
	for _, lane := range []string{"", "1"} {
		t.Setenv("CLOWN_NIX_TEST_LANE", lane)
		var r skipRecorder
		skipAllowed(&r, "real-binary smoke")
		if r.skipped != "allowlisted skip: real-binary smoke" || r.failed != "" {
			t.Fatalf("lane %q: skipped %q failed %q, want the allowlisted skip", lane, r.skipped, r.failed)
		}
		r = skipRecorder{}
		skipAllowed(&r, " ")
		if r.skipped != "" || r.failed == "" {
			t.Fatalf("lane %q, blank reason: skipped %q failed %q, want a failure", lane, r.skipped, r.failed)
		}
	}
}

func TestSkipOrFail(t *testing.T) {
	t.Setenv("CLOWN_NIX_TEST_LANE", "")
	var r skipRecorder
	skipOrFail(&r, "no llama-server")
	if r.skipped != "no llama-server" || r.failed != "" {
		t.Fatalf("outside the lane: skipped %q failed %q, want a skip", r.skipped, r.failed)
	}
	t.Setenv("CLOWN_NIX_TEST_LANE", "1")
	r = skipRecorder{}
	skipOrFail(&r, "no llama-server")
	if r.skipped != "" || r.failed == "" {
		t.Fatalf("in the lane: skipped %q failed %q, want a failure", r.skipped, r.failed)
	}
}

package testlane

import (
	"fmt"
	"strings"
	"testing"
)

type recorder struct {
	skipped, failed string
}

func (r *recorder) Helper() {}

func (r *recorder) Skipf(format string, args ...any) {
	r.skipped = fmt.Sprintf(format, args...)
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
}

// An allowlisted skip skips in and out of the lane, carrying its reason;
// one without a reason fails, so every allowlisted skip says why.
func TestSkipAllowed(t *testing.T) {
	for _, lane := range []string{"", "1"} {
		t.Setenv(EnvVar, lane)
		var r recorder
		SkipAllowed(&r, "real-binary smoke, not a lane test")
		if r.skipped != "allowlisted skip: real-binary smoke, not a lane test" || r.failed != "" {
			t.Fatalf("lane %q: skipped %q failed %q, want the allowlisted skip", lane, r.skipped, r.failed)
		}
		r = recorder{}
		SkipAllowed(&r, "  ")
		if r.skipped != "" || r.failed == "" {
			t.Fatalf("lane %q, blank reason: skipped %q failed %q, want a failure", lane, r.skipped, r.failed)
		}
	}
}

// Outside the nix test lane a missing prerequisite skips; inside it, where
// every prerequisite is supposed to be provided, it fails (clown#235).
func TestSkipOrFail(t *testing.T) {
	t.Setenv(EnvVar, "")
	var r recorder
	SkipOrFail(&r, "git not on PATH: %v", "boom")
	if r.skipped != "git not on PATH: boom" || r.failed != "" {
		t.Fatalf("outside the lane: skipped %q failed %q, want a skip", r.skipped, r.failed)
	}

	t.Setenv(EnvVar, "1")
	r = recorder{}
	SkipOrFail(&r, "git not on PATH: %v", "boom")
	if r.skipped != "" || r.failed == "" {
		t.Fatalf("in the lane: skipped %q failed %q, want a failure", r.skipped, r.failed)
	}
	if !strings.Contains(r.failed, "git not on PATH: boom") || !strings.Contains(r.failed, EnvVar) {
		t.Fatalf("failure %q should carry the reason and name %s", r.failed, EnvVar)
	}
}

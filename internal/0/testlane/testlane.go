// Package testlane lets a test's prerequisite guard skip on a developer's
// machine but fail in the nix test lane, where every prerequisite is meant
// to be provided (clown#235): a guard that silently skips there leaves the
// code it guards untested while the suite stays green.
package testlane

import (
	"fmt"
	"os"
	"strings"
)

// EnvVar is set to "1" by the nix test lane (flake.nix mkClownGodynTests).
const EnvVar = "CLOWN_NIX_TEST_LANE"

// TB is the part of testing.TB that SkipOrFail uses.
type TB interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// InLane reports whether the test runs in the nix test lane.
func InLane() bool { return os.Getenv(EnvVar) == "1" }

// SkipAllowed skips t in and out of the lane: an allowlisted skip, for a
// test the lane deliberately does not run. reason is required (a blank one
// fails the test) and is logged with the skip, so every allowlisted skip is
// greppable (testlane.SkipAllowed) and says why.
func SkipAllowed(t TB, reason string) {
	t.Helper()
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("testlane.SkipAllowed needs a reason")
		return
	}
	t.Skipf("allowlisted skip: %s", reason)
}

// SkipOrFail skips t with the reason, or fails it in the nix test lane: a
// prerequisite the lane should provide is missing there.
func SkipOrFail(t TB, format string, args ...any) {
	t.Helper()
	if InLane() {
		t.Fatalf("%s (%s=1: the nix test lane must provide this prerequisite, not skip)", fmt.Sprintf(format, args...), EnvVar)
		return
	}
	t.Skipf(format, args...)
}

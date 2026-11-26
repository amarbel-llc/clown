package main

import (
	"strings"
	"testing"
	"time"
)

// The reason recorded when the serve made no socket (read by the operator in
// `sc list`) follows the one-switch model: it names spinclass's
// [confinement] bridge setting and what to check, and no second gate (review of
// spinclass's cut, B-M4). Red with the old text, which spoke of "spinclass's own
// gate for it" and a "trusted [bridge] enable".
func TestNoBridgeSocketReasonNamesTheOneSwitch(t *testing.T) {
	got := noBridgeSocketReason(10 * time.Second)
	for _, want := range []string{"created no bridge socket in 10s", "[confinement] bridge", "check that the serve"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q lacks %q", got, want)
		}
	}
	for _, stale := range []string{"own gate", "second gate", "trusted [bridge]", "alone starts no listener"} {
		if strings.Contains(got, stale) {
			t.Errorf("reason %q still speaks of a second gate (%q)", got, stale)
		}
	}
}

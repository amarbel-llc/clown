package main

import (
	"strings"
	"testing"
)

// With both identity parts known, the fragment leads with this session's own
// FQ ID and carries the unconditional MUST rule with the FQ shape spelled out.
func TestFQIdentityFragmentFull(t *testing.T) {
	got := fqIdentityFragment("clown/rare-redwood", "pogo")
	for _, want := range []string{
		"`clown/rare-redwood/pogo`",
		"`<repo>/<spinclass-session>/<clown>`",
		"MUST",
		"NEVER a bare clown-name",
		"chat_list",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment missing %q:\n%s", want, got)
		}
	}
}

// A session missing its group or name still gets the rule for referring to
// OTHERS, is told to identify itself by the parts it has, and no FQ ID is
// fabricated for it.
func TestFQIdentityFragmentDegraded(t *testing.T) {
	for _, tc := range []struct {
		name             string
		groupID, clownID string
	}{
		{"no group", "", "pogo"},
		{"no name", "clown/rare-redwood", ""},
		{"neither", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fqIdentityFragment(tc.groupID, tc.clownID)
			if !strings.Contains(got, "MUST") || !strings.Contains(got, "`<repo>/<spinclass-session>/<clown>`") {
				t.Errorf("degraded fragment must still carry the FQ rule:\n%s", got)
			}
			if strings.Contains(got, "Your fully-qualified session ID is") {
				t.Errorf("degraded fragment must not claim an FQ ID:\n%s", got)
			}
			if !strings.Contains(got, "never fabricate") {
				t.Errorf("degraded fragment must forbid fabricating the missing parts:\n%s", got)
			}
		})
	}
}

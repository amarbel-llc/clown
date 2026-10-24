package main

import "testing"

// clown#233 (option 1): the OSC-2 title goes to stderr, so the title gate
// also requires stderr to be a terminal; the [attach] wrap gate does not.
func TestTitleGateRequiresAStderrTerminal(t *testing.T) {
	cases := []struct {
		name                           string
		interactive, stderrTTY, forced bool
		want                           bool
	}{
		{"interactive terminal", true, true, false, true},
		{"only stderr redirected (clown -p x 2>log)", true, false, false, false},
		{"no terminal at all", false, false, false, false},
		{"stderr a terminal but stdin/stdout not", false, true, false, false},
		{"CLOWN_ATTACH_FORCE=1 still overrides", false, false, true, true},
	}
	for _, c := range cases {
		if got := titleGate(c.interactive, c.stderrTTY, c.forced); got != c.want {
			t.Errorf("%s: titleGate = %v, want %v", c.name, got, c.want)
		}
	}
}

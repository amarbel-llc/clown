package main

import "testing"

// An unattended session never prompts, even on a pty; --skip-failed still
// lets it continue; an attended terminal still gets the prompt (clown#346).
func TestPluginFailurePolicy(t *testing.T) {
	cases := []struct {
		name        string
		skipFailed  bool
		interactive bool
		unattended  bool
		want        pluginFailureAction
	}{
		{"attended terminal prompts", false, true, false, failPrompt},
		{"attended without a terminal aborts", false, false, false, failAbort},
		{"unattended on a pty never prompts", false, true, true, failAbort},
		{"unattended without a pty aborts", false, false, true, failAbort},
		{"skip-failed continues attended", true, true, false, failSkip},
		{"skip-failed continues unattended on a pty", true, true, true, failSkip},
		{"skip-failed continues without a terminal", true, false, false, failSkip},
	}
	for _, c := range cases {
		if got := pluginFailurePolicy(c.skipFailed, c.interactive, c.unattended); got != c.want {
			t.Errorf("%s: policy = %v, want %v", c.name, got, c.want)
		}
	}
}

package main

import (
	"slices"
	"testing"

	"code.linenisgreat.com/clown/internal/0/promptwalk"
)

// Operator ruling 2026-10-27: an unattended session may not start monitors of
// its own (the Monitor tool runs an arbitrary shell command and feeds its
// output back, an escape from the permission profile). The two monitors clown
// declares itself, the wake monitor and troupe receive, are plugin monitors
// and are not started through the tool, so denying the tool name leaves them.
// The deny is added at launch from the posture, not by a profile, so it covers
// whatever profile the session was spawned with and moves no profile digest.
func TestUnattendedLaunchDisallowsTheMonitorTool(t *testing.T) {
	cases := []struct {
		name       string
		unattended string
		carried    bool
		profile    string
		wantTool   bool
	}{
		{"attended", "", false, "", false},
		{"unattended", "1", false, "", true},
		{"unattended through the [attach] carrier", "", true, "", true},
		{"unattended with the profile's own list", "1", false, "Artifact,Workflow", true},
		{"attended with a profile list", "", false, "Artifact", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(unattendedEnv, c.unattended)
			t.Setenv("CLOWN_DISALLOWED_TOOLS", c.profile)
			old := unattendedCarried
			unattendedCarried = c.carried
			t.Cleanup(func() { unattendedCarried = old })

			got := claudeLaunchArgs("claude", "", parsedFlags{}, nil, promptwalk.PromptResult{}, nil).ExtraDisallowed
			if has := slices.Contains(got, "Monitor"); has != c.wantTool {
				t.Fatalf("ExtraDisallowed = %q, Monitor present %v, want %v", got, has, c.wantTool)
			}
			for _, want := range profileDisallowedTools() {
				if !slices.Contains(got, want) {
					t.Errorf("the profile's %q was dropped: %q", want, got)
				}
			}
		})
	}
}

// A profile that already lists Monitor does not get it twice.
func TestUnattendedMonitorDenyIsNotDuplicated(t *testing.T) {
	t.Setenv(unattendedEnv, "1")
	t.Setenv("CLOWN_DISALLOWED_TOOLS", "Monitor,Workflow")
	got := launchDisallowedTools()
	n := 0
	for _, tool := range got {
		if tool == "Monitor" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Monitor appears %d times in %q, want once", n, got)
	}
}

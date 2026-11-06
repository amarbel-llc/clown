package main

import (
	"strings"
	"testing"
)

// clown#242: a relaunched session is told that jobs it started before the
// restart outlive it but their completion wake does not, so it does not wait
// for a wake that cannot come.
func TestRestartedNoticeWarnsAboutPreRestartJobs(t *testing.T) {
	got := restartedNotice("new build")
	for _, want := range []string{
		"restarted: new build",
		"Background jobs you started before this restart outlive it, but their completion wake does not",
		"Do not wait for a wake from a job started before the restart",
		"treat a state of running as unknown",
		"ringmaster(1) CAVEATS",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notice lacks %q:\n%s", want, got)
		}
	}
	if restartedNotice("") != "" {
		t.Error("a launch that is not a relaunch gets no notice")
	}
}

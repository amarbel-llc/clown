package main

import (
	"strings"
	"testing"
)

// clown#242: a relaunched session is told that jobs it started before the
// restart keep running but their completion wake does not, so it does not
// wait for a wake that cannot come.
func TestRestartedNoticeWarnsAboutPreRestartJobs(t *testing.T) {
	got := restartedNotice("new build")
	for _, want := range []string{
		"restarted: new build",
		"Jobs started before this restart keep running but their wake died with the session",
		"do not wait",
		"treat running as unknown",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notice lacks %q:\n%s", want, got)
		}
	}
	if restartedNotice("") != "" {
		t.Error("a launch that is not a relaunch gets no notice")
	}
	// The whole notice, with the longest reason sanitizeRestartReason keeps,
	// stays under the bound TestRestartedNoticeIsOneBoundedLine pins.
	if long := restartedNotice(strings.Repeat("x", 5000)); len(long) >= 1000 {
		t.Errorf("notice with a capped reason is %d bytes; want under 1000", len(long))
	}
}

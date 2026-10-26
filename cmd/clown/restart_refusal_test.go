package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/internal/sessionstatus"
)

func recordRefusals() (*[]sessionstatus.RestartRefusal, func(sessionstatus.RestartRefusal)) {
	var got []sessionstatus.RestartRefusal
	return &got, func(r sessionstatus.RestartRefusal) { got = append(got, r) }
}

func readRefusal(t *testing.T, principal string) (sessionstatus.RestartRefusal, error) {
	t.Helper()
	st, ok := readStatusFile(t, principal)
	if !ok || st.RestartRefusal == nil {
		return sessionstatus.RestartRefusal{}, errors.New("no refusal recorded")
	}
	return *st.RestartRefusal, nil
}

// A restart dropped after spinclass consumed it leaves a record a holder can
// read, one per drop, with the reason for it: the dry-run profile gate, the
// countdown, and a turn that started during it.
func TestCancelledRestartLeavesARefusalRecord(t *testing.T) {
	providerRunning.Store(true)
	t.Cleanup(func() { providerRunning.Store(false) })

	for _, c := range []struct {
		name     string
		fr       func(record func(sessionstatus.RestartRefusal)) frontendRestarter
		verdict  preflightVerdict
		wantKind string
		wantText string
	}{
		{
			"profile gate",
			func(record func(sessionstatus.RestartRefusal)) frontendRestarter {
				return frontendRestarter{principal: restartTestPrincipal, profile: "worker", refusal: record, profileCheck: func() error { return errors.New("profile worker changed") }}
			},
			preflightVerdict{Verdict: "restart", When: "idle"},
			sessionstatus.RefusalProfileGate, "profile worker changed",
		},
		{
			"countdown",
			func(record func(sessionstatus.RestartRefusal)) frontendRestarter {
				return frontendRestarter{principal: restartTestPrincipal, refusal: record, countdown: func(string, bool) (bool, string) { return false, "cancelled by the operator" }}
			},
			preflightVerdict{Verdict: "restart", When: "idle"},
			sessionstatus.RefusalCountdown, "cancelled by the operator",
		},
		{
			"turn started",
			func(record func(sessionstatus.RestartRefusal)) frontendRestarter {
				return frontendRestarter{principal: restartTestPrincipal, refusal: record, busy: func() bool { return true }}
			},
			preflightVerdict{Verdict: "restart", When: "idle"},
			sessionstatus.RefusalTurnStarted, "a turn started during the countdown",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, record := recordRefusals()
			c.fr(record).restart(restartRequest{When: "idle"}, c.verdict)
			if len(*got) != 1 || (*got)[0].Kind != c.wantKind || !strings.Contains((*got)[0].Text, c.wantText) {
				t.Fatalf("refusals = %+v, want one %s containing %q", *got, c.wantKind, c.wantText)
			}
			if c.name == "profile gate" && (*got)[0].Profile != "worker" {
				t.Errorf("profile = %q, want the frontend's profile", (*got)[0].Profile)
			}
		})
	}
}

// A restart that goes ahead leaves no refusal.
func TestCarriedOutRestartLeavesNoRefusal(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	got, record := recordRefusals()
	fr := frontendRestarter{principal: restartTestPrincipal, refusal: record, stop: func() {}, stopGrace: 1}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", When: "idle"})
	if len(*got) != 0 {
		t.Fatalf("refusals = %+v", *got)
	}
}

// The relaunch's own resume gate refusing is counted and written to the
// principal's status file; any other launch records nothing.
func TestRelaunchRefusalIsCountedAndWritten(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "repo/branch")
	counted := recordMetrics(t)
	flags := parsedFlags{provider: "claude", profile: "worker"}
	flags.identity.Key = restartTestPrincipal

	noteRelaunchRefused(false, flags, errors.New("refused"))
	if g := counted(); len(g) != 0 {
		t.Fatalf("a first launch counted %q", g)
	}
	if _, err := readRefusal(t, restartTestPrincipal); err == nil {
		t.Fatal("a first launch wrote a refusal")
	}

	noteRelaunchRefused(true, flags, errors.New("profile worker changed\nsince the session was pinned"))
	if g := counted(); !slices.Equal(g, []string{metricRestartRefused}) {
		t.Fatalf("counted %q", g)
	}
	r, err := readRefusal(t, restartTestPrincipal)
	if err != nil || r.Kind != sessionstatus.RefusalResume || r.Profile != "worker" || !strings.Contains(r.Text, "profile worker changed") || strings.Contains(r.Text, "\n") {
		t.Fatalf("refusal = %+v, %v", r, err)
	}
}

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/alfa/sessionstatus"
)

// A restart the countdown cancels because a turn started is deferred to the
// next idle transition, not dropped: the verdict spinclass already issued is
// kept and acted on again, without a second preflight (a second ask returns
// "none"). The bound is 3 attempts or restartDeferWindow from the first
// verdict, whichever comes first, and only the final cancel is recorded.

var errBoom = errors.New("profile worker changed")

type deferHarness struct {
	fr       frontendRestarter
	q        *restartQueue
	idle     *idleRunner
	refusals *[]sessionstatus.RestartRefusal
	stops    *int
	now      *time.Time
	calls    *int
}

// newDeferHarness builds a restarter whose countdown is countdown(call), call
// counting from 1, over a queue with a fake clock.
func newDeferHarness(t *testing.T, countdown func(call int) (bool, string), busy func() bool) *deferHarness {
	t.Helper()
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	now := time.Unix(1_000_000, 0)
	calls, stops := 0, 0
	q := &restartQueue{clock: func() time.Time { return now }}
	got, record := recordRefusals()
	fr := frontendRestarter{
		principal: restartTestPrincipal,
		queue:     q,
		refusal:   record,
		logPath:   t.TempDir() + "/l.log",
		busy:      busy,
		stop:      func() { stops++ },
		stopGrace: time.Hour,
		kill:      func() error { return nil },
		countdown: func(string, bool) (bool, string) { calls++; return countdown(calls) },
	}
	idle := &idleRunner{queue: q, restart: fr.restart, logPath: fr.logPath}
	return &deferHarness{fr: fr, q: q, idle: idle, refusals: got, stops: &stops, now: &now, calls: &calls}
}

var deferTestVerdict = preflightVerdict{Verdict: "restart", Reason: "r", When: "idle"}

func (h *deferHarness) first() {
	h.fr.restart(restartRequest{When: "idle", Reason: "r"}, deferTestVerdict)
}

func alwaysTurnCancelled(int) (bool, string) {
	return false, "posh has no app modal; " + turnStartedNote
}

func TestTurnCancelledRestartIsDeferredNotDropped(t *testing.T) {
	h := newDeferHarness(t, alwaysTurnCancelled, nil)
	h.first()
	if !h.q.hasDeferred() {
		t.Fatal("a turn-cancelled restart was dropped, not deferred")
	}
	if len(*h.refusals) != 0 {
		t.Fatalf("a deferred restart recorded a refusal: %+v", *h.refusals)
	}
	if *h.stops != 0 || pendingRelaunch.Load() != nil {
		t.Fatal("a cancelled attempt stopped the provider or stored a plan")
	}
}

func TestDeferredRestartIsRetriedAtTheNextIdleWithoutAPreflight(t *testing.T) {
	h := newDeferHarness(t, func(call int) (bool, string) {
		if call == 1 {
			return alwaysTurnCancelled(call)
		}
		return true, "inline countdown elapsed"
	}, nil)
	h.first()
	h.idle.preflightQueued() // the Stop that ends the turn; h.idle.preflight is nil, so a preflight would panic
	if *h.stops != 1 || pendingRelaunch.Load() == nil {
		t.Fatalf("stops %d, plan %v; the retry did not restart", *h.stops, pendingRelaunch.Load())
	}
	if h.q.hasDeferred() {
		t.Fatal("a restarted session still holds a deferred restart")
	}
	if len(*h.refusals) != 0 {
		t.Fatalf("a restart that went ahead left a refusal: %+v", *h.refusals)
	}
}

// A retry never stops the provider while a turn is running: a turn that is
// busy again after the retry's countdown defers it once more.
func TestRetryNeverStopsMidTurn(t *testing.T) {
	busy := true // the turn that started during the countdown is still running
	h := newDeferHarness(t, func(int) (bool, string) { return true, "inline countdown elapsed" }, func() bool { return busy })
	h.first()
	if *h.stops != 0 || !h.q.hasDeferred() {
		t.Fatalf("stops %d, deferred %v; want deferred with no stop", *h.stops, h.q.hasDeferred())
	}
	h.idle.preflightQueued() // a retry attempted while still busy
	if *h.stops != 0 {
		t.Fatal("a retry stopped the provider mid-turn")
	}
	busy = false
	h.idle.preflightQueued()
	if *h.stops != 1 {
		t.Fatalf("stops %d after the turn ended, want 1", *h.stops)
	}
}

func TestRetryIsBoundedAndOnlyTheFinalCancelIsRecorded(t *testing.T) {
	h := newDeferHarness(t, alwaysTurnCancelled, nil)
	h.first()
	h.idle.preflightQueued()
	if len(*h.refusals) != 0 || !h.q.hasDeferred() {
		t.Fatalf("after attempt 2: refusals %+v, deferred %v", *h.refusals, h.q.hasDeferred())
	}
	h.idle.preflightQueued() // attempt 3, the last
	if len(*h.refusals) != 1 || (*h.refusals)[0].Kind != sessionstatus.RefusalCountdown {
		t.Fatalf("refusals = %+v, want exactly one countdown refusal on the last cancel", *h.refusals)
	}
	if !strings.Contains((*h.refusals)[0].Text, "attempt 3 of 3") {
		t.Errorf("refusal text %q does not name the attempts", (*h.refusals)[0].Text)
	}
	if h.q.hasDeferred() {
		t.Fatal("a restart over its bound is still deferred")
	}
	n := *h.calls
	h.idle.preflightQueued()
	if *h.calls != n || *h.stops != 0 {
		t.Fatal("an exhausted restart was retried again")
	}
}

func TestRetryExpiresAfterTheWindow(t *testing.T) {
	h := newDeferHarness(t, alwaysTurnCancelled, nil)
	h.first()
	*h.now = h.now.Add(restartDeferWindow + time.Second) // the turn outlasted the window
	n := *h.calls
	h.idle.preflightQueued()
	if *h.calls != n {
		t.Fatal("an expired restart ran its countdown again")
	}
	if len(*h.refusals) != 1 || !strings.Contains((*h.refusals)[0].Text, "retry window") {
		t.Fatalf("refusals = %+v, want one naming the retry window", *h.refusals)
	}
	if *h.stops != 0 {
		t.Fatal("an expired restart stopped the provider")
	}
}

// Only a turn start is retried; an operator's cancel and every other refusal
// are final at once.
func TestOnlyATurnStartIsRetried(t *testing.T) {
	for name, why := range map[string]string{
		"viewer cancel": "posh modal: cancelled by a viewer",
		"other":         "cancelled by the operator",
	} {
		t.Run(name, func(t *testing.T) {
			h := newDeferHarness(t, func(int) (bool, string) { return false, why }, nil)
			h.first()
			if h.q.hasDeferred() || len(*h.refusals) != 1 {
				t.Fatalf("deferred %v, refusals %+v; want a final refusal", h.q.hasDeferred(), *h.refusals)
			}
		})
	}
	t.Run("profile gate", func(t *testing.T) {
		h := newDeferHarness(t, alwaysTurnCancelled, nil)
		h.fr.profileCheck = func() error { return errBoom }
		h.first()
		if h.q.hasDeferred() || len(*h.refusals) != 1 || (*h.refusals)[0].Kind != sessionstatus.RefusalProfileGate {
			t.Fatalf("deferred %v, refusals %+v", h.q.hasDeferred(), *h.refusals)
		}
	})
}

// The profile gate is checked again on the retry: a profile that changed in
// between is a final profile-gate refusal, not a restart.
func TestRetryRechecksTheProfileGate(t *testing.T) {
	h := newDeferHarness(t, alwaysTurnCancelled, nil)
	h.first()
	h.fr.profileCheck = func() error { return errBoom }
	h.idle.restart = h.fr.restart
	h.idle.preflightQueued()
	if len(*h.refusals) != 1 || (*h.refusals)[0].Kind != sessionstatus.RefusalProfileGate || *h.stops != 0 {
		t.Fatalf("refusals %+v, stops %d", *h.refusals, *h.stops)
	}
}

// A newer request replaces a deferred one: it is preflighted afresh.
func TestNewerRequestSupersedesADeferredRestart(t *testing.T) {
	h := newDeferHarness(t, alwaysTurnCancelled, nil)
	h.first()
	h.q.put(restartRequest{When: "idle", Reason: "newer"})
	if h.q.hasDeferred() {
		t.Fatal("a deferred restart survived a newer request")
	}
}

// A `now` verdict never cancels on a turn, so it is never deferred.
func TestNowVerdictIsNeverDeferred(t *testing.T) {
	var cancelOnTurn []bool
	h := newDeferHarness(t, func(int) (bool, string) { return true, "ok" }, func() bool { return true })
	h.fr.countdown = func(_ string, c bool) (bool, string) { cancelOnTurn = append(cancelOnTurn, c); return true, "ok" }
	h.fr.restart(restartRequest{When: "now"}, preflightVerdict{Verdict: "restart", Reason: "r", When: "now"})
	if h.q.hasDeferred() || *h.stops != 1 || len(cancelOnTurn) != 1 || cancelOnTurn[0] {
		t.Fatalf("deferred %v, stops %d, cancelOnTurn %v", h.q.hasDeferred(), *h.stops, cancelOnTurn)
	}
}

// The countdown names a turn start with turnStartedNote on every path, and
// never a viewer's cancel.
func TestCountdownNamesATurnStartConsistently(t *testing.T) {
	probeOKPosh := &fakePosh{probeOut: probeOK}
	for name, c := range map[string]restartCountdown{
		"inline": {interactive: true, notify: &strings.Builder{}, sleep: func(time.Duration) {}, busy: func() bool { return true }},
		"posh modal": {
			poshSession: "sess", interactive: true, notify: &strings.Builder{}, sleep: func(time.Duration) {}, busy: func() bool { return true },
			lookPath: func(n string) (string, error) { return "/bin/" + n, nil },
			run:      probeOKPosh.run,
		},
	} {
		t.Run(name, func(t *testing.T) {
			proceed, how := c.proceed("r", true)
			if proceed || !strings.HasSuffix(how, turnStartedNote) {
				t.Fatalf("proceed %v, how %q; want a turn cancel ending %q", proceed, how, turnStartedNote)
			}
		})
	}
	viewer := restartCountdown{
		poshSession: "sess", interactive: true,
		lookPath: func(n string) (string, error) { return "/bin/" + n, nil },
		run:      (&fakePosh{probeOut: probeOK, modalCode: 10}).run,
	}
	if _, how := viewer.proceed("r", true); strings.HasSuffix(how, turnStartedNote) {
		t.Fatalf("a viewer's cancel reads as a turn start: %q", how)
	}
}

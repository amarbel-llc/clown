package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/alfa/sessionstatus"
)

// The P1 stop verdict: spinclass answers restart-preflight with "stop" and the
// frontend stops the provider without relaunching (spinclass's stop right).

func resetStopState(t *testing.T) {
	t.Helper()
	pendingRelaunch.Store(nil)
	pendingStop.Store(false)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); pendingStop.Store(false); providerRunning.Store(false) })
}

func TestParsePreflightVerdictAcceptsStop(t *testing.T) {
	v, err := parsePreflightVerdict([]byte(`{"verdict":"stop","reason":"parked by a holder","when":"idle"}` + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Verdict != verdictStop || v.Reason != "parked by a holder" || v.When != "idle" {
		t.Fatalf("verdict = %+v", v)
	}
}

// The preflight tells spinclass which verdicts this clown understands, so an
// older clown, which cannot stop, is never sent "stop" (it would consume the
// request and do nothing).
func TestPreflightAdvertisesTheVerdictsItUnderstands(t *testing.T) {
	dir := t.TempDir()
	envOut := filepath.Join(dir, "verdicts")
	bin := filepath.Join(dir, "spinclass")
	script := "#!/bin/sh\nprintf '%s' \"$CLOWN_RESTART_VERDICTS\" > '" + envOut + "'\necho '{\"verdict\":\"none\",\"reason\":\"\",\"when\":\"\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOWN_RESTART_VERDICTS", "restart") // an inherited value must not leak through
	p := &restartPreflight{bin: bin, principal: restartTestPrincipal, logPath: filepath.Join(dir, "l.log")}
	if _, err := p.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	b, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(string(b), ",")
	for _, want := range []string{"restart", "none", "refused", "stop"} {
		if !containsToken(got, want) {
			t.Fatalf("CLOWN_RESTART_VERDICTS = %q, missing %q", b, want)
		}
	}
}

func containsToken(tokens []string, want string) bool {
	for _, tok := range tokens {
		if tok == want {
			return true
		}
	}
	return false
}

// The idle runner hands a stop verdict to the restarter, as it does a restart.
func TestIdleVerdictStopReachesTheRestarter(t *testing.T) {
	bin, _ := fakeIdleSpinclass(t, `{"verdict":"stop","reason":"parked","when":"idle"}`+"\n", "0")
	q := &restartQueue{}
	q.put(restartRequest{When: "idle"})
	var got atomic.Pointer[preflightVerdict]
	r := newTestIdleRunner(t, bin, q, &recordedRestarts{})
	r.restart = func(_ restartRequest, v preflightVerdict) { got.Store(&v) }
	r.onIdle()
	if v := got.Load(); v == nil || v.Verdict != verdictStop {
		t.Fatalf("restarter got %+v, want the stop verdict", v)
	}
}

// A stop stops the provider and stores no relaunch plan, so main exits; the
// relaunch's own gates (profile, posture, countdown) are not consulted, and
// the session counts as underway so no idle poke or `now` starts anything.
func TestStopVerdictStopsWithoutARelaunch(t *testing.T) {
	resetStopState(t)
	stops, gates := 0, 0
	fr := frontendRestarter{
		principal:    restartTestPrincipal,
		stop:         func() { stops++ },
		logPath:      filepath.Join(t.TempDir(), "l.log"),
		profileCheck: func() error { gates++; return nil },
		postureCheck: func(string) error { gates++; return nil },
		countdown:    func(string, bool) (bool, string) { gates++; return true, "elapsed" },
		kill:         func() error { return nil },
		stopGrace:    time.Hour,
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: verdictStop, Reason: "parked", When: "idle"})
	if stops != 1 {
		t.Fatalf("stops = %d, want 1", stops)
	}
	if pendingRelaunch.Load() != nil {
		t.Fatal("a stop must store no relaunch plan")
	}
	if gates != 0 {
		t.Fatalf("the relaunch gates ran %d times for a stop", gates)
	}
	if !restartUnderway() {
		t.Fatal("a stopping session must count as underway")
	}
}

// A provider that already exited is not stopped, and nothing is marked.
func TestStopVerdictWithNoProviderDoesNothing(t *testing.T) {
	resetStopState(t)
	providerRunning.Store(false)
	stops := 0
	fr := frontendRestarter{principal: restartTestPrincipal, stop: func() { stops++ }, logPath: filepath.Join(t.TempDir(), "l.log")}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: verdictStop, When: "idle"})
	if stops != 0 || pendingStop.Load() {
		t.Fatalf("stops %d, stopping %v; no provider was running", stops, pendingStop.Load())
	}
}

// Under an idle stop a running turn is not killed: the stop is deferred to the
// next idle transition, as a restart is, and retried as a stop.
func TestStopVerdictDefersWhileATurnRuns(t *testing.T) {
	resetStopState(t)
	stops := 0
	busy := true
	q := &restartQueue{}
	fr := frontendRestarter{
		principal: restartTestPrincipal,
		stop:      func() { stops++ },
		busy:      func() bool { return busy },
		queue:     q,
		logPath:   filepath.Join(t.TempDir(), "l.log"),
		kill:      func() error { return nil },
		stopGrace: time.Hour,
	}
	req, v := restartRequest{When: "idle"}, preflightVerdict{Verdict: verdictStop, Reason: "parked", When: "idle"}
	fr.restart(req, v)
	if stops != 0 || !q.hasDeferred() {
		t.Fatalf("stops %d, deferred %v; a busy session must be deferred, not stopped", stops, q.hasDeferred())
	}
	busy = false
	dreq, dv, ok := q.takeDeferred()
	if !ok || dv.Verdict != verdictStop {
		t.Fatalf("deferred = %+v, %v; want the stop kept", dv, ok)
	}
	fr.restart(dreq, dv)
	if stops != 1 || pendingRelaunch.Load() != nil {
		t.Fatalf("retry: stops %d, plan %v; want a stop without a relaunch", stops, pendingRelaunch.Load())
	}
}

// Under spinclass's `now` stop a running turn is expected and stopped.
func TestStopVerdictNowStopsDespiteATurn(t *testing.T) {
	resetStopState(t)
	stops := 0
	fr := frontendRestarter{
		principal: restartTestPrincipal,
		stop:      func() { stops++ },
		busy:      func() bool { return true },
		logPath:   filepath.Join(t.TempDir(), "l.log"),
		kill:      func() error { return nil },
		stopGrace: time.Hour,
	}
	fr.restart(restartRequest{When: "now"}, preflightVerdict{Verdict: verdictStop, When: "now"})
	if stops != 1 {
		t.Fatalf("stops = %d, want 1", stops)
	}
}

// A retried stop that meets a running merge/check job is the final cancel,
// recorded, and the provider keeps running.
func TestStopVerdictRetryBlockedByARunningJob(t *testing.T) {
	resetStopState(t)
	stops := 0
	var refusals []sessionstatus.RestartRefusal
	q := &restartQueue{}
	fr := frontendRestarter{
		principal:  restartTestPrincipal,
		stop:       func() { stops++ },
		queue:      q,
		logPath:    filepath.Join(t.TempDir(), "l.log"),
		jobRunning: func() (string, bool) { return "merge job m-1", true },
		refusal:    func(r sessionstatus.RestartRefusal) { refusals = append(refusals, r) },
	}
	req, v := restartRequest{When: "idle"}, preflightVerdict{Verdict: verdictStop, When: "idle"}
	q.deferRestart(deferredRestart{req: req, v: v, first: time.Now(), attempts: 1, kind: sessionstatus.RefusalTurnStarted, why: "a turn was running"})
	dreq, dv, _ := q.takeDeferred()
	fr.restart(dreq, dv)
	if stops != 0 || pendingStop.Load() {
		t.Fatalf("stops %d, stopping %v; a running job must block the retried stop", stops, pendingStop.Load())
	}
	if len(refusals) != 1 {
		t.Fatalf("refusals = %+v, want one", refusals)
	}
}

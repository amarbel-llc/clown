package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// R1: while a restart is underway (the plan is stored), an idle poke runs
// nothing: no close-self-run, no preflight.
func TestIdleDuringARestartRunsNothing(t *testing.T) {
	t.Cleanup(func() { pendingRelaunch.Store(nil) })
	bin, calls := fakeIdleSpinclass(t, `{"verdict":"restart","reason":"r","when":"idle"}`+"\n", "0")
	q := &restartQueue{}
	q.put(restartRequest{When: "idle"})
	var rec recordedRestarts
	r := newTestIdleRunner(t, bin, q, &rec)
	pendingRelaunch.Store(&relaunchPlan{principal: restartTestPrincipal})
	r.onIdle()
	if got := readCalls(t, calls); len(got) != 0 {
		t.Fatalf("calls = %q; a restart underway must run no close and no preflight", got)
	}
	if rec.n.Load() != 0 {
		t.Fatal("restarted again")
	}
}

// R1/R4: a second restart while one is underway is refused, not queued.
func TestControlRefusesARestartWhileOneIsUnderway(t *testing.T) {
	t.Cleanup(func() { pendingRelaunch.Store(nil) })
	pendingRelaunch.Store(&relaunchPlan{principal: restartTestPrincipal})
	q := &restartQueue{}
	reply := restartControlHandler("claude", "", q)(controlMessage{Op: "restart", When: "idle", Reason: "again"})
	if reply.OK || !strings.Contains(reply.Error, "already") {
		t.Fatalf("reply = %+v, want a refusal naming the restart underway", reply)
	}
	if _, ok := q.take(); ok {
		t.Fatal("queued during a restart")
	}
}

// R1: a session closed while the restart waited (its worktree is gone) is
// not relaunched.
func TestRelaunchSkipsASessionClosedDuringTheRestart(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "worktree")
	var calls []execCall
	var stderr bytes.Buffer
	r := testRelauncher(t, shortSocketDir(t), &calls)
	r.stderr = &stderr
	code := r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: gone})
	if len(calls) != 0 {
		t.Fatal("relaunched a closed session")
	}
	if code != 0 || !strings.Contains(stderr.String(), "closed") {
		t.Fatalf("code %d, stderr %q; want a clean exit saying the session was closed", code, stderr.String())
	}
}

// R1: a session still there after the stop is relaunched.
func TestRelaunchRelaunchesALiveSession(t *testing.T) {
	var calls []execCall
	r := testRelauncher(t, shortSocketDir(t), &calls)
	r.stderr = &bytes.Buffer{}
	r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: t.TempDir()})
	if len(calls) != 1 {
		t.Fatalf("exec calls = %d, want one", len(calls))
	}
}

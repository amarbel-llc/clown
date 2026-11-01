package jugglerrun

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// stubSystemdChecker reports r (or err) without shelling out.
type stubSystemdChecker struct {
	r   SystemdReadiness
	err error
}

func (s stubSystemdChecker) IsSystemRunning(context.Context) (SystemdReadiness, error) {
	return s.r, s.err
}

func TestCheckSystemdReady_AcceptsRunningAndDegraded(t *testing.T) {
	for _, state := range []string{"running", "degraded"} {
		if err := CheckSystemdReady(context.Background(), stubSystemdChecker{r: SystemdReadiness{State: state}}); err != nil {
			t.Errorf("state %q: err = %v", state, err)
		}
	}
}

func TestCheckSystemdReady_RefusesEveryOtherState(t *testing.T) {
	for _, state := range []string{"starting", "initializing", "maintenance", "stopping", "offline", "unknown", ""} {
		err := CheckSystemdReady(context.Background(), stubSystemdChecker{r: SystemdReadiness{State: state}})
		var notReady *ErrSystemdNotReady
		if !errors.As(err, &notReady) {
			t.Fatalf("state %q: err = %v, want *ErrSystemdNotReady", state, err)
		}
		want := state
		if want == "" {
			want = "unknown"
		}
		if notReady.State != want {
			t.Errorf("state %q: notReady.State = %q, want %q", state, notReady.State, want)
		}
		if err.Error() != `systemd is not ready to start units: is-system-running reported "`+want+`"` {
			t.Errorf("state %q: message = %q", state, err.Error())
		}
	}
}

// TestCheckSystemdReady_NoStateCarriesDetailInsteadOfBeingLossy covers the
// case systemctl exits non-zero with nothing on stdout at all (D-Bus/polkit
// down, no user bus under --user): the checker's own explanation (Detail)
// must survive into the refusal and its message, not collapse into a bare
// "unknown" indistinguishable from systemd genuinely reporting that state.
func TestCheckSystemdReady_NoStateCarriesDetailInsteadOfBeingLossy(t *testing.T) {
	checker := stubSystemdChecker{r: SystemdReadiness{Detail: "systemctl exit 1: Failed to connect to bus: no such file or directory"}}
	err := CheckSystemdReady(context.Background(), checker)
	var notReady *ErrSystemdNotReady
	if !errors.As(err, &notReady) {
		t.Fatalf("err = %v, want *ErrSystemdNotReady", err)
	}
	if notReady.State != "unknown" || notReady.Detail != checker.r.Detail {
		t.Errorf("notReady = %+v", notReady)
	}
	wantMsg := `systemd is not ready to start units: is-system-running reported no state (systemctl exit 1: Failed to connect to bus: no such file or directory)`
	if err.Error() != wantMsg {
		t.Errorf("message = %q, want %q", err.Error(), wantMsg)
	}
}

func TestCheckSystemdReady_PropagatesACheckerThatCouldNotRun(t *testing.T) {
	cause := errors.New("exec: \"systemctl\": executable file not found in $PATH")
	err := CheckSystemdReady(context.Background(), stubSystemdChecker{err: cause})
	var notReady *ErrSystemdNotReady
	if errors.As(err, &notReady) {
		t.Fatalf("a checker failure must not be reported as a refusal: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want it to wrap %v", err, cause)
	}
}

func TestCheckSystemdReady_MissingCheckerNeverSilentlyPasses(t *testing.T) {
	if err := CheckSystemdReady(context.Background(), missingSystemdChecker{}); !errors.Is(err, errNoSystemdChecker) {
		t.Errorf("err = %v, want it to wrap errNoSystemdChecker", err)
	}
}

func TestSpawnChild_RefusedWhenSystemdNotReady(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	mints := mintCalls(t, h)
	h.fakes.SetSystemctlState(t, "maintenance")

	_, err := spawnForNarrows(h, testTask)
	var notReady *ErrSystemdNotReady
	if !errors.As(err, &notReady) || notReady.State != "maintenance" || notReady.Detail != "" {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != `systemd is not ready to start units: is-system-running reported "maintenance"` {
		t.Errorf("message = %q", err.Error())
	}
	if n := mintCalls(t, h); n != mints {
		t.Errorf("nothing may be minted: %d mints became %d", mints, n)
	}
	// The precondition precedes the narrowing check too: no side effect of
	// THIS spawn may run before it.
	if len(h.fakes.Calls(t, "moxy")) != 0 {
		t.Error("the narrowing check must not run before the systemd precondition")
	}
	if len(h.fakes.Calls(t, "systemd-run")) != 0 {
		t.Error("nothing may be started")
	}

	data, err := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err != nil {
		t.Fatal(err)
	}
	var l RunLedger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	refusals := callsOf(l, "systemd")
	if len(refusals) != 1 || refusals[0].Kind != "refusal" || refusals[0].OK || refusals[0].State != "maintenance" || refusals[0].Detail != "" {
		t.Errorf("ledger = %s", data)
	}
}

func TestSpawnChild_ACheckThatCannotReadAnyStateRefusesAsUnknown(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	h.fakes.Fail(t, "systemctl", "is-system-running")

	_, err := spawnForNarrows(h, testTask)
	wantMsg := `systemd is not ready to start units: is-system-running reported no state (systemctl exit 1: systemctl is-system-running: injected failure)`
	if err == nil || err.Error() != wantMsg {
		t.Fatalf("err = %v, want %q", err, wantMsg)
	}
	data, err := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err != nil {
		t.Fatal(err)
	}
	var l RunLedger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	refusals := callsOf(l, "systemd")
	wantDetail := "systemctl exit 1: systemctl is-system-running: injected failure"
	if len(refusals) != 1 || refusals[0].State != "unknown" || refusals[0].Detail != wantDetail {
		t.Errorf("ledger = %s", data)
	}
}

func TestSpawnChild_ProceedsUnderDegradedSystemd(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	systemctlBefore := len(h.fakes.Calls(t, "systemctl"))
	h.fakes.SetSystemctlState(t, "degraded")

	rec, err := spawnForNarrows(h, testTask)
	if err != nil {
		t.Fatalf("degraded must still be allowed to spawn: %v", err)
	}
	if len(h.fakes.Calls(t, "systemd-run")) != 1 || rec.Unit == "" {
		t.Error("the unit must still start under a degraded systemd")
	}
	if got := len(h.fakes.Calls(t, "systemctl")) - systemctlBefore; got != 1 {
		t.Errorf("systemctl calls = %d, want exactly 1 (the readiness check actually ran)", got)
	}
}

func TestSpawnChild_ExistingChildReturnsWithoutCheckingSystemd(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	if _, err := spawnForNarrows(h, testTask); err != nil {
		t.Fatal(err)
	}
	systemctlCallsBefore := len(h.fakes.Calls(t, "systemctl"))
	h.fakes.SetSystemctlState(t, "maintenance")

	// The same brief and task is the same idempotency key: nothing new is
	// started, so a currently degraded-beyond-repair systemd must not
	// refuse the (no-op) call.
	if _, err := spawnForNarrows(h, testTask); err != nil {
		t.Fatalf("an idempotent re-spawn must not be refused: %v", err)
	}
	if got := len(h.fakes.Calls(t, "systemctl")); got != systemctlCallsBefore {
		t.Errorf("an idempotent re-spawn must not re-check systemd: %d calls became %d", systemctlCallsBefore, got)
	}
}

func TestNewRun_RefusedWhenSystemdNotReady(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	mints := mintCalls(t, h)
	h.fakes.SetSystemctlState(t, "starting")

	_, err := NewRun(context.Background(), h.deps, NewRunRequest{
		RunKey: "rk-not-ready", Issuer: testIssuer, Input: []byte(`{"recording":"r"}`), Room: "run@rooms.test",
	})
	var notReady *ErrSystemdNotReady
	if !errors.As(err, &notReady) || notReady.State != "starting" {
		t.Fatalf("err = %v", err)
	}
	if n := mintCalls(t, h); n != mints {
		t.Errorf("nothing may be minted: %d mints became %d", mints, n)
	}
	if rec, err := h.deps.Store.LoadRun("rk-not-ready"); err != nil {
		t.Fatal(err)
	} else if rec != nil {
		t.Errorf("no run record may be saved before any side effect: %+v", rec)
	}
	if len(h.fakes.Calls(t, "troupe")) != 0 {
		t.Error("nothing may be posted")
	}
}

func TestNewRun_ProceedsUnderDegradedSystemd(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	h.fakes.SetSystemctlState(t, "degraded")

	res, err := NewRun(context.Background(), h.deps, NewRunRequest{
		RunKey: "rk-degraded", Issuer: testIssuer, Input: []byte(`{"recording":"r"}`), Room: "run@rooms.test",
	})
	if err != nil {
		t.Fatalf("degraded must still be allowed to start a run: %v", err)
	}
	if res.RootPrincipal != testRoot {
		t.Errorf("res = %+v", res)
	}
	if got := len(h.fakes.Calls(t, "systemctl")); got != 1 {
		t.Errorf("systemctl calls = %d, want exactly 1 (the readiness check actually ran)", got)
	}
}

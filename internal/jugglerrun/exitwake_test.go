package jugglerrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// countingDoneRingmaster makes the first failCount calls to Done fail with
// err, then delegates to the wrapped Ringmaster; it also counts every call
// (clown#257: pins ExitWake's done-retry behavior).
type countingDoneRingmaster struct {
	Ringmaster
	failCount int
	err       error
	calls     int
}

func (r *countingDoneRingmaster) Done(ctx context.Context, target, job string, d DoneRecord) error {
	r.calls++
	if r.calls <= r.failCount {
		return r.err
	}
	return r.Ringmaster.Done(ctx, target, job, d)
}

// wroteButErroredRingmaster simulates a `done` call whose write actually
// landed on the journal before the call itself reported a failure — e.g. a
// connection reset right after the server-side write succeeded (clown#257
// review finding 2, scenario B). It delegates the real write to the
// wrapped Ringmaster first, then overrides a successful result with err.
type wroteButErroredRingmaster struct {
	Ringmaster
	err   error
	calls int
}

func (r *wroteButErroredRingmaster) Done(ctx context.Context, target, job string, d DoneRecord) error {
	r.calls++
	if err := r.Ringmaster.Done(ctx, target, job, d); err != nil {
		return err
	}
	return r.err
}

// startAgentJob starts a job on root-1's channel holding root-1 (accepted)
// and a pending grantee that must never be woken.
func startAgentJob(t *testing.T, h *harness) string {
	t.Helper()
	ctx := context.Background()
	job, err := h.deps.Ringmaster.Start(ctx, testRoot, AgentJobLabel, SpawnSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := mirrorHolder(ctx, h.deps.Ringmaster, testRoot, job, FirstHolder(testRoot, time.Now())); err != nil {
		t.Fatal(err)
	}
	pending := Holder{Principal: "grantee", Rights: DefaultRights, Status: HandlePending, GrantedBy: testRoot}
	if err := mirrorHolder(ctx, h.deps.Ringmaster, testRoot, job, pending); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestExitWake_ServiceResultTable(t *testing.T) {
	cases := []struct {
		result string
		state  string
		reason ExitReason
		msg    string
	}{
		{"success", StateInterrupted, ReasonCrash, "crash: "},
		{"timeout", StateFailed, ReasonFailed, "wall clock (RuntimeMaxSec) expired"},
		{"signal", StateInterrupted, ReasonKilled, "killed: "},
		{"exit-code", StateInterrupted, ReasonCrash, "crash: "},
		{"core-dump", StateInterrupted, ReasonCrash, "crash: "},
		{"oom-kill", StateInterrupted, ReasonCrash, "crash: "},
	}
	for _, tc := range cases {
		t.Run(tc.result, func(t *testing.T) {
			h := newHarness(t)
			job := startAgentJob(t, h)
			req := ExitWakeRequest{Job: job, Target: testRoot, From: "agent-1", Service: ServiceResult{Result: tc.result, ExitCode: "killed", ExitStatus: "KILL"}}

			out, err := ExitWake(context.Background(), h.deps, req)
			if err != nil {
				t.Fatalf("ExitWake: %v", err)
			}
			if out.State != tc.state || out.Reason != tc.reason || !strings.HasPrefix(out.Message, tc.msg) || !out.WroteTerminal {
				t.Fatalf("outcome = %+v", out)
			}
			recs := h.fakes.Records(t, testRoot, job)
			if term := lastRecord(t, recs); term.Type != tc.state {
				t.Errorf("terminal = %+v", term)
			}
			wakes := h.fakes.Wakes(t)
			if len(wakes) != 1 || wakes[0].Target != testRoot || wakes[0].From != "agent-1" || wakes[0].Source != ExitWakeSource {
				t.Fatalf("wakes = %+v (pending holders get none)", wakes)
			}
			if want := "exit " + job + " " + tc.state + " reason=" + string(tc.reason) + " "; !strings.HasPrefix(wakes[0].Message, want) {
				t.Errorf("wake text = %q, want prefix %q", wakes[0].Message, want)
			}

			// Idempotent: a second run writes nothing and wakes nobody.
			again, err := ExitWake(context.Background(), h.deps, req)
			if err != nil {
				t.Fatalf("second ExitWake: %v", err)
			}
			if again.WroteTerminal || !again.AlreadyWoken || again.State != tc.state || again.Reason != tc.reason {
				t.Errorf("second outcome = %+v", again)
			}
			if n := countTerminals(h.fakes.Records(t, testRoot, job)); n != 1 {
				t.Errorf("terminal records = %d, want 1", n)
			}
			if n := len(h.fakes.Wakes(t)); n != 1 {
				t.Errorf("wakes = %d, want 1", n)
			}
		})
	}
}

func TestExitWake_RelaysTheRunsVerdict(t *testing.T) {
	h := newHarness(t)
	job := startAgentJob(t, h)
	h.fakes.AppendRecord(t, testRoot, job, StateFailed, `cannot_complete: "no repo"`, "/spool/ledger")

	out, err := ExitWake(context.Background(), h.deps, ExitWakeRequest{Job: job, Target: testRoot, Service: ServiceResult{Result: "exit-code", ExitCode: "exited", ExitStatus: "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.WroteTerminal || out.State != StateFailed || out.Reason != ReasonFailed {
		t.Fatalf("outcome = %+v", out)
	}
	wakes := h.fakes.Wakes(t)
	if len(wakes) != 1 || wakes[0].ResultRef != "/spool/ledger" || !strings.Contains(wakes[0].Message, `reason=failed cannot_complete: "no repo"`) {
		t.Errorf("wakes = %+v", wakes)
	}
}

func TestExitWake_SucceededRelaysNormal(t *testing.T) {
	h := newHarness(t)
	job := startAgentJob(t, h)
	h.fakes.AppendRecord(t, testRoot, job, StateSucceeded, "evaluator passed", "")
	out, err := ExitWake(context.Background(), h.deps, ExitWakeRequest{Job: job, Target: testRoot, Service: ServiceResult{Result: "success"}})
	if err != nil || out.State != StateSucceeded || out.Reason != ReasonNormal || out.WroteTerminal {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
}

func TestExitWake_AbortedIsShutdown(t *testing.T) {
	h := newHarness(t)
	job := startAgentJob(t, h)
	h.fakes.AppendRecord(t, testRoot, job, StateAborted, "cancelled by a holder", "")
	out, err := ExitWake(context.Background(), h.deps, ExitWakeRequest{Job: job, Target: testRoot, Service: ServiceResult{Result: "exit-code"}})
	if err != nil || out.Reason != ReasonShutdown {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
}

func TestExitWake_UnknownJobIsAnError(t *testing.T) {
	h := newHarness(t)
	if _, err := ExitWake(context.Background(), h.deps, ExitWakeRequest{Job: "nope-1", Target: testRoot}); err == nil {
		t.Fatal("want an error for a job with no journal")
	}
}

// TestExitWake_RetriesATransientDoneFailure covers clown#257: a `done`
// failure that is NOT jobwake.ErrAlreadyTerminal (ringmaster transiently
// unreachable, say) must be retried, not refused on the first bad call —
// the hook is the terminal writer of last resort. A generous ctx deadline
// and a tiny backoff keep this deterministic (not racing wall-clock load).
func TestExitWake_RetriesATransientDoneFailure(t *testing.T) {
	h := newHarness(t)
	job := startAgentJob(t, h)
	transient := &countingDoneRingmaster{Ringmaster: h.deps.Ringmaster, failCount: 3, err: errors.New("ringmaster: transient failure")}
	deps := h.deps
	deps.Ringmaster = transient
	deps.DoneRetryBackoff = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := ExitWake(ctx, deps, ExitWakeRequest{Job: job, Target: testRoot, Service: ServiceResult{Result: "exit-code"}})
	if err != nil {
		t.Fatalf("ExitWake: %v", err)
	}
	if !out.WroteTerminal || out.State != StateInterrupted {
		t.Fatalf("outcome = %+v", out)
	}
	if transient.calls != 4 {
		t.Errorf("Done was called %d times, want exactly 4 (3 failures + 1 success)", transient.calls)
	}
	if n := countTerminals(h.fakes.Records(t, testRoot, job)); n != 1 {
		t.Errorf("terminal records = %d, want exactly 1", n)
	}
}

// TestExitWake_GivesUpWhenDoneNeverSucceeds covers clown#257's other half:
// a `done` call that fails forever must not strand the hook in an infinite
// loop — it gives up once ctx is done, returns an error (which cmdExitWake
// logs and turns into a non-zero exit), and never writes a terminal record
// of its own (the journal stays exactly as it was: no terminal at all).
func TestExitWake_GivesUpWhenDoneNeverSucceeds(t *testing.T) {
	h := newHarness(t)
	job := startAgentJob(t, h)
	alwaysFails := &countingDoneRingmaster{Ringmaster: h.deps.Ringmaster, failCount: 1 << 30, err: errors.New("ringmaster: permanently down")}
	deps := h.deps
	deps.Ringmaster = alwaysFails
	deps.DoneRetryBackoff = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out, err := ExitWake(ctx, deps, ExitWakeRequest{Job: job, Target: testRoot, Service: ServiceResult{Result: "exit-code"}})
	if err == nil {
		t.Fatal("want an error when done never succeeds")
	}
	if !strings.Contains(err.Error(), "giving up") || !strings.Contains(err.Error(), "permanently down") {
		t.Errorf("err = %v, want it to name the cause and that it gave up", err)
	}
	if out.WroteTerminal {
		t.Errorf("outcome = %+v, want WroteTerminal false", out)
	}
	if alwaysFails.calls < 2 {
		t.Errorf("Done was called %d times, want at least one retry", alwaysFails.calls)
	}
	if n := countTerminals(h.fakes.Records(t, testRoot, job)); n != 0 {
		t.Errorf("terminal records = %d, want 0: the journal must stay untouched", n)
	}
}

// TestExitWake_RelaysATerminalItsOwnDoneWroteBeforeReportingFailure covers
// clown#257 review finding 2 (scenario B): a `done` call that actually
// wrote the terminal record before reporting a failure (e.g. a connection
// reset right after the server-side write landed) must relay that
// terminal instead of retrying into a duplicate attempt. A short ctx
// deadline well below DoneRetryBackoff makes this deterministic: if the
// fix regresses and the code retries anyway, the call fails fast on the
// deadline instead of hanging, and the call-count/terminal-count
// assertions below still catch it.
func TestExitWake_RelaysATerminalItsOwnDoneWroteBeforeReportingFailure(t *testing.T) {
	h := newHarness(t)
	job := startAgentJob(t, h)
	wroteButErrored := &wroteButErroredRingmaster{Ringmaster: h.deps.Ringmaster, err: errors.New("ringmaster: connection reset")}
	deps := h.deps
	deps.Ringmaster = wroteButErrored
	deps.DoneRetryBackoff = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out, err := ExitWake(ctx, deps, ExitWakeRequest{Job: job, Target: testRoot, Service: ServiceResult{Result: "exit-code"}})
	if err != nil {
		t.Fatalf("ExitWake: %v", err)
	}
	if out.WroteTerminal {
		t.Error("WroteTerminal = true, want false: the terminal was found by re-reading the journal, not reported as this call's own success")
	}
	if out.State != StateInterrupted {
		t.Fatalf("outcome = %+v", out)
	}
	if wroteButErrored.calls != 1 {
		t.Errorf("Done was called %d times, want exactly 1 (no retry once the re-read found the terminal it wrote)", wroteButErrored.calls)
	}
	if n := countTerminals(h.fakes.Records(t, testRoot, job)); n != 1 {
		t.Errorf("terminal records = %d, want exactly 1: no duplicate from a retry", n)
	}
}

func TestExitReasonFor(t *testing.T) {
	cases := map[[2]string]ExitReason{
		{StateSucceeded, ""}:                  ReasonNormal,
		{StateFailed, "x"}:                    ReasonFailed,
		{StateAborted, ""}:                    ReasonShutdown,
		{"cancelled", ""}:                     ReasonShutdown,
		{StateInterrupted, "killed: SIGKILL"}: ReasonKilled,
		{StateInterrupted, "crash: oom"}:      ReasonCrash,
		{StateInterrupted, ""}:                ReasonCrash,
		{StateRunning, ""}:                    "",
	}
	for in, want := range cases {
		if got := ExitReasonFor(in[0], in[1]); got != want {
			t.Errorf("ExitReasonFor(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestHandles(t *testing.T) {
	now := time.Now()
	holders := []Holder{FirstHolder("a", now), {Principal: "b", Rights: DefaultRights, Status: HandlePending}, FirstHolder("c", now)}
	if got := WakeRecipients(holders); strings.Join(got, ",") != "a,c" {
		t.Errorf("recipients = %v", got)
	}
	if got := ReleaseHolder(holders, "a"); len(got) != 2 || got[0].Principal != "b" {
		t.Errorf("release removed the wrong holder: %+v", got)
	}
	if holders[0].Rights != "observe,close" {
		t.Errorf("default rights = %q", holders[0].Rights)
	}
	var recs []JobRecord
	for _, h := range append(holders, Holder{Principal: "c", Status: HandleReleased}) {
		msg, err := HolderProgressMessage(h)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, JobRecord{Type: "progress", Message: msg})
	}
	recs = append(recs, JobRecord{Type: "progress", Message: "turn t1 text"})
	rebuilt := HoldersFromRecords(recs)
	if len(rebuilt) != 2 || rebuilt[0].Principal != "a" || rebuilt[1].Principal != "b" {
		t.Errorf("rebuilt = %+v", rebuilt)
	}
}

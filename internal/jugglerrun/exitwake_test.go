package jugglerrun

import (
	"context"
	"strings"
	"testing"
	"time"
)

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

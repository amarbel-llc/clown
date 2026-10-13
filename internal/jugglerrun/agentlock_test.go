package jugglerrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// orderRecordingRingmaster wraps a Ringmaster and appends "start"/"done" to a
// shared, mutex-guarded log, so a test can assert the lock's acquire/release
// calls landed between them.
type orderRecordingRingmaster struct {
	Ringmaster
	mu  *sync.Mutex
	log *[]string
}

func (r orderRecordingRingmaster) Start(ctx context.Context, target, label, source string) (string, error) {
	job, err := r.Ringmaster.Start(ctx, target, label, source)
	r.mu.Lock()
	*r.log = append(*r.log, "start")
	r.mu.Unlock()
	return job, err
}

func (r orderRecordingRingmaster) Done(ctx context.Context, target, job string, d DoneRecord) error {
	err := r.Ringmaster.Done(ctx, target, job, d)
	r.mu.Lock()
	*r.log = append(*r.log, "done")
	r.mu.Unlock()
	return err
}

func TestRunAgent_HoldsTheJobLockBetweenStartAndDone(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	bin, _ := fakeMoxyBin(t, fakeMoxy(t))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir()

	var mu sync.Mutex
	var log []string
	deps.Ringmaster = orderRecordingRingmaster{Ringmaster: deps.Ringmaster, mu: &mu, log: &log}
	var acquired, released int
	deps.AcquireJobLock = func(target, jobID string) (func() error, error) {
		if target != testRoot || jobID == "" {
			t.Errorf("AcquireJobLock(%q, %q): want target %q and a non-empty job id", target, jobID, testRoot)
		}
		mu.Lock()
		acquired++
		log = append(log, "acquire")
		mu.Unlock()
		return func() error {
			mu.Lock()
			released++
			log = append(log, "release")
			mu.Unlock()
			return nil
		}, nil
	}

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "agent-1"})
	if err != nil || out.State != StateSucceeded {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if acquired != 1 || released != 1 {
		t.Fatalf("acquired = %d, released = %d, want 1 and 1", acquired, released)
	}
	if want := []string{"start", "acquire", "done", "release"}; !reflect.DeepEqual(log, want) {
		t.Errorf("order = %v, want %v (the lock must be held across the whole run, acquired right after start and released only after done)", log, want)
	}
}

// TestRunAgent_LockFailureIsNotFatal covers an acquire failure OTHER than
// jobwake.ErrAlreadyLocked (e.g. the jobwake wiring unset, or ringmaster's
// job-lock directory unwritable): the run still proceeds and its terminal is
// still written, it just loses the reaper backstop — recorded in the
// ledger's Notes, not only stderr (clown#256 review finding 5). Contrast
// TestRunAgent_ErrAlreadyLockedRefusesWithoutWritingTheJournal, where losing
// the lock to a live holder refuses instead.
func TestRunAgent_LockFailureIsNotFatal(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	bin, _ := fakeMoxyBin(t, fakeMoxy(t))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir()
	var released int
	deps.AcquireJobLock = func(string, string) (func() error, error) {
		return func() error { released++; return nil }, errors.New("job-lock directory unwritable")
	}

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "agent-1"})
	if err != nil || out.State != StateSucceeded {
		t.Fatalf("a lock the agent could not acquire (not ErrAlreadyLocked) must not fail the run: outcome = %+v, err = %v", out, err)
	}
	if released != 0 {
		t.Fatalf("released = %d, want 0: AcquireJobLock returned no release func on this failure, so finish must not invent one", released)
	}
	found := false
	for _, r := range h.fakes.Records(t, testRoot, out.Job) {
		if r.Type == "succeeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("job %s has no succeeded terminal: Done must still be written when only the lock failed to acquire", out.Job)
	}
	if len(out.Ledger.Notes) == 0 {
		t.Fatal("ledger.Notes is empty: the lost backstop must be recorded for an auditor, not only logged to stderr")
	}
}

// TestRunAgent_ErrAlreadyLockedRefusesWithoutWritingTheJournal: losing the
// job's advisory lock to a live holder (jobwake.ErrAlreadyLocked) means
// another producer is genuinely, concurrently working this same job.
// RunAgent must refuse outright and write NOTHING — not even a `failed`
// terminal through the usual finishWithoutLoop path — because ringmaster
// settles a job's terminal first-write-wins (RFC-0018 §2.4): a terminal from
// the losing duplicate could durably clobber the real holder's own, correct
// outcome (clown#256 review finding 5).
func TestRunAgent_ErrAlreadyLockedRefusesWithoutWritingTheJournal(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	bin, _ := fakeMoxyBin(t, fakeMoxy(t))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir()
	// Pre-start the job the way `juggler spawn` does, so RunAgent adopts it
	// via req.Job instead of starting its own.
	job, err := deps.Ringmaster.Start(context.Background(), testRoot, AgentJobLabel, SpawnSource)
	if err != nil {
		t.Fatal(err)
	}
	deps.AcquireJobLock = func(string, string) (func() error, error) {
		return nil, jobwake.ErrAlreadyLocked
	}

	out, runErr := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), Job: job, EnvPrincipal: "agent-1"})
	if !errors.Is(runErr, jobwake.ErrAlreadyLocked) {
		t.Fatalf("err = %v, want it to wrap jobwake.ErrAlreadyLocked", runErr)
	}
	if out.Job != "" || out.State != "" || out.LedgerPath != "" {
		t.Fatalf("outcome = %+v, want the zero value: nothing should be written when the lock is already held by a live producer", out)
	}
	for _, r := range h.fakes.Records(t, testRoot, job) {
		if r.Type != "started" {
			t.Fatalf("journal got record %+v from the losing duplicate; it must write nothing beyond the pre-existing `started` record", r)
		}
	}
}

// TestRunAgent_HoldsTheJobLockForAnAdoptedJob covers the `--job <id>` path
// (`juggler spawn` already started the job; Start is skipped, see
// agent.go's `if a.job == ""`): the lock must still be acquired right after
// the job is addressed and released only after Done.
func TestRunAgent_HoldsTheJobLockForAnAdoptedJob(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	bin, _ := fakeMoxyBin(t, fakeMoxy(t))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir()
	job, err := deps.Ringmaster.Start(context.Background(), testRoot, AgentJobLabel, SpawnSource)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var log []string
	deps.Ringmaster = orderRecordingRingmaster{Ringmaster: deps.Ringmaster, mu: &mu, log: &log}
	var acquired, released int
	deps.AcquireJobLock = func(target, jobID string) (func() error, error) {
		if target != testRoot || jobID != job {
			t.Errorf("AcquireJobLock(%q, %q): want (%q, %q)", target, jobID, testRoot, job)
		}
		mu.Lock()
		acquired++
		log = append(log, "acquire")
		mu.Unlock()
		return func() error {
			mu.Lock()
			released++
			log = append(log, "release")
			mu.Unlock()
			return nil
		}, nil
	}

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), Job: job, EnvPrincipal: "agent-1"})
	if err != nil || out.State != StateSucceeded {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if acquired != 1 || released != 1 {
		t.Fatalf("acquired = %d, released = %d, want 1 and 1", acquired, released)
	}
	// No "start": RunAgent adopts the pre-started job rather than starting
	// its own (orderRecordingRingmaster only logs Start/Done).
	if want := []string{"acquire", "done", "release"}; !reflect.DeepEqual(log, want) {
		t.Errorf("order = %v, want %v", log, want)
	}
}

// TestRunAgent_AdoptedJobProtocolMismatchRefusesBeforeAcquiringLock covers
// the adopted-job (`--job <id>`) protocol-check path (clown#256 review
// finding 3) together with its delta-review regression (finding 1): a
// mismatch must be a bare error with AcquireJobLock never called — not a
// `finishWithoutLoop` that goes on to call Done through the SAME checked
// Ringmaster, which would silently write nothing (the cached check error
// refuses every method identically) while still claiming a terminal via the
// exit code.
func TestRunAgent_AdoptedJobProtocolMismatchRefusesBeforeAcquiringLock(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	h.fakes.SetRingmasterProtocolVersion(t, "2")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	deps.Ringmaster = &ProtocolCheckedRingmaster{Inner: deps.Ringmaster, Bin: h.fakes.Ringmaster}
	deps.AcquireJobLock = func(string, string) (func() error, error) {
		t.Fatal("AcquireJobLock must not be called: the adopted-job protocol check must run first and refuse before the lock is ever taken")
		return nil, nil
	}

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), Job: "adopted-job-1", EnvPrincipal: "agent-1"})
	if err == nil || !strings.Contains(err.Error(), "protocol 2") {
		t.Fatalf("err = %v, want a protocol-mismatch error naming protocol 2", err)
	}
	if out.Job != "" || out.State != "" || out.LedgerPath != "" {
		t.Fatalf("outcome = %+v, want the zero value: nothing should be written on a protocol mismatch", out)
	}
	for _, c := range h.fakes.Calls(t, "ringmaster") {
		if len(c.Argv) > 0 && c.Argv[0] == "done" {
			t.Fatalf("ringmaster calls include %v; want no done call — the mismatch must refuse before any write is attempted", c.Argv)
		}
	}
}

// TestRunAgent_ReleasesTheLockOnFinishWithoutLoop covers a run that never
// reaches the loop at all (no model resolver configured): the lock must
// still be released, since finishWithoutLoop routes through the same finish
// as every other terminal path.
func TestRunAgent_ReleasesTheLockOnFinishWithoutLoop(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(rm.ResolveModelResult{})
	deps.ResolveModel = nil
	var released int
	deps.AcquireJobLock = func(string, string) (func() error, error) {
		return func() error { released++; return nil }, nil
	}

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "agent-1"})
	if err != nil || out.State != StateFailed {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1: the lock must be released even when the run never reaches the loop", released)
	}
}

// failingDoneRingmaster makes every Done call fail with a fixed error,
// everything else forwarded to the embedded Ringmaster unchanged.
type failingDoneRingmaster struct {
	Ringmaster
	err error
}

func (r failingDoneRingmaster) Done(context.Context, string, string, DoneRecord) error { return r.err }

// TestRunAgent_ReleasesTheLockEvenWhenDoneFails: a Done call that fails for
// a reason OTHER than jobwake.ErrAlreadyTerminal (a transport error, say)
// must not strand the lock held — the next AcquireJobLock on the same job
// (e.g. a retry) would otherwise find it still taken.
func TestRunAgent_ReleasesTheLockEvenWhenDoneFails(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	bin, _ := fakeMoxyBin(t, fakeMoxy(t))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir()
	deps.Ringmaster = failingDoneRingmaster{Ringmaster: deps.Ringmaster, err: errors.New("boom")}
	var released int
	deps.AcquireJobLock = func(string, string) (func() error, error) {
		return func() error { released++; return nil }, nil
	}

	if _, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "agent-1"}); err != nil {
		t.Fatalf("err = %v, want nil: RunAgent still returns a verdict when Done merely fails to write", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1: the lock must still be released when Done errors", released)
	}
}

// TestRunAgent_RelaysTheTerminalThatWonTheRace: when the agent's own Done
// call loses the race against the v3 liveness reaper (jobwake.ErrAlreadyTerminal),
// RunAgent's outcome must reflect the terminal that actually landed on the
// journal, not the agent's own verdict, which was never written (clown#256
// review finding 4).
func TestRunAgent_RelaysTheTerminalThatWonTheRace(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	bin, _ := fakeMoxyBin(t, fakeMoxy(t))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir()
	h.fakes.RaceDoneReaperOnce(t)

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "agent-1"})
	if err != nil {
		t.Fatalf("err = %v, want nil: a losing race is relayed, not a failure", err)
	}
	const wantMessage = "producer died without writing a terminal record"
	if out.State != StateInterrupted || out.Message != wantMessage {
		t.Fatalf("outcome = %+v, want State %q and Message %q: the reaper's terminal, not the agent's own (which lost the race and was never written)", out, StateInterrupted, wantMessage)
	}
	recs := h.fakes.Records(t, testRoot, out.Job)
	if n := countTerminals(recs); n != 1 {
		t.Fatalf("journal has %d terminal records, want exactly 1 (the reaper's, first-write-wins)", n)
	}
	if last := lastRecord(t, recs); last.Type != StateInterrupted || last.Source != "fake-reaper" {
		t.Fatalf("last record = %+v, want the reaper's interrupted terminal", last)
	}
}

func TestExecRingmaster_DoneAlreadyTerminalWrapsErrAlreadyTerminal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	job, err := h.deps.Ringmaster.Start(ctx, testRoot, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.deps.Ringmaster.Done(ctx, testRoot, job, DoneRecord{State: StateSucceeded}); err != nil {
		t.Fatal(err)
	}
	// A second done against the now-terminal job must be reported the same
	// way ringmaster v3's own `done` exit 3 means it (clown#256): settled,
	// not failed.
	err = h.deps.Ringmaster.Done(ctx, testRoot, job, DoneRecord{State: StateFailed})
	if !errors.Is(err, jobwake.ErrAlreadyTerminal) {
		t.Fatalf("err = %v, want it to wrap jobwake.ErrAlreadyTerminal", err)
	}
}

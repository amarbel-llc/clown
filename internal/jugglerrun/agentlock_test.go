package jugglerrun

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

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

func TestRunAgent_LockFailureIsNotFatal(t *testing.T) {
	h := newHarness(t)
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	bin, _ := fakeMoxyBin(t, fakeMoxy(t))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir()
	deps.AcquireJobLock = func(string, string) (func() error, error) {
		return nil, jobwake.ErrAlreadyLocked
	}

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "agent-1"})
	if err != nil || out.State != StateSucceeded {
		t.Fatalf("a lock the agent could not acquire must not fail the run: outcome = %+v, err = %v", out, err)
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

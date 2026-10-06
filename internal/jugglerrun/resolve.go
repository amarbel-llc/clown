package jugglerrun

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// ResolveRequest is `juggler resolve`.
type ResolveRequest struct {
	RunJob string
	// State is succeeded, or failed when the fallback ran.
	State  string
	Reason string
	// FallbackArtifacts is the fallback script's own outcome.
	FallbackArtifacts []Artifact
}

// ResolveOutcome is `juggler resolve`'s stdout object.
type ResolveOutcome struct {
	RunKey string     `json:"run_key"`
	RunJob string     `json:"run_job"`
	State  string     `json:"state"`
	Reason ExitReason `json:"reason"`
	Ledger string     `json:"ledger"`
	Woken  []string   `json:"woken"`
	// AlreadyResolved: the run was resolved before; nothing was rewritten.
	AlreadyResolved bool `json:"already_resolved"`
}

// Resolve is the run's last call (FDR 0019 §1): it appends the `fallback`
// entry to the run ledger, writes the ledger to the store and the run job's
// spool, writes the run job's terminal record and sends the run's exit wake
// to the run job's holders (the issuer). Every step is retry-safe: the
// ledger entry is appended once (the ledger's resolved field gates it), an
// existing terminal record is relayed, and the wake marker prevents a
// second wake.
func Resolve(ctx context.Context, deps LifecycleDeps, req ResolveRequest) (ResolveOutcome, error) {
	deps = deps.withDefaults()
	if req.State != StateSucceeded && req.State != StateFailed {
		return ResolveOutcome{}, fmt.Errorf("--state must be %s or %s, got %q", StateSucceeded, StateFailed, req.State)
	}
	run, err := deps.Store.FindRunByJob(req.RunJob)
	if err != nil {
		return ResolveOutcome{}, err
	}
	if run == nil {
		return ResolveOutcome{}, fmt.Errorf("no run record has run job %q", req.RunJob)
	}
	unlock, err := lock(deps.Store.runPath(run.RunKey) + ".lock")
	if err != nil {
		return ResolveOutcome{}, err
	}
	defer unlock()
	out := ResolveOutcome{RunKey: run.RunKey, RunJob: run.RunJob, Ledger: deps.Store.RunLedgerPath(run.RunKey), Woken: []string{}}
	if run.Resolved != nil {
		out.State, out.Reason, out.AlreadyResolved = run.Resolved.State, ExitReasonFor(run.Resolved.State, ""), true
		return out, nil
	}

	ledger, err := deps.Store.loadRunLedger(run.RunKey)
	if err != nil {
		return out, err
	}
	if ledger.Resolved == nil {
		artifacts := req.FallbackArtifacts
		if artifacts == nil {
			artifacts = []Artifact{}
		}
		ledger.Calls = append(ledger.Calls, FallbackEntry(req.State, req.Reason, artifacts))
		ledger.Resolved = &Resolution{State: req.State, Reason: req.Reason, At: deps.Now().UTC()}
		if err := writeJSONAtomic(out.Ledger, ledger); err != nil {
			return out, fmt.Errorf("writing the run ledger: %w", err)
		}
	}
	doc, err := marshalDocument(ledger)
	if err != nil {
		return out, err
	}
	resultRef := out.Ledger
	if spool, err := writeSpool(ctx, deps.Ringmaster, run.Issuer, run.RunJob, doc); err != nil {
		fmt.Fprintf(os.Stderr, "juggler: resolve: %v\n", err)
	} else if spool != "" {
		resultRef = spool
	}

	state, message := ledger.Resolved.State, ledger.Resolved.Reason
	doneErr := deps.Ringmaster.Done(ctx, run.Issuer, run.RunJob, DoneRecord{State: state, Message: message, ResultRef: resultRef})
	recs, err := deps.Ringmaster.Records(ctx, run.Issuer, run.RunJob)
	if err != nil {
		return out, fmt.Errorf("reading %s: %w", run.RunJob, err)
	}
	term, terminal := TerminalRecord(recs)
	if !terminal {
		return out, fmt.Errorf("ringmaster done %s: %w", run.RunJob, errors.Join(doneErr, errors.New("no terminal record")))
	}
	out.State = term.Type
	out.Reason = ExitReasonFor(term.Type, term.Message)

	out.Woken, _, err = sendExitWakes(ctx, deps, exitWakeBatch{
		Job:        run.RunJob,
		From:       run.RootPrincipal,
		Source:     ResolveSource,
		State:      term.Type,
		Reason:     out.Reason,
		Message:    term.Message,
		ResultRef:  term.ResultRef,
		Recipients: WakeRecipients(MergeHolders(run.Holders, HoldersFromRecords(recs))),
	})
	if err != nil {
		return out, err
	}
	run.Resolved = ledger.Resolved
	if err := deps.Store.SaveRun(run); err != nil {
		return out, fmt.Errorf("saving the run record: %w", err)
	}
	return out, nil
}

// lookupJob validates job and finds the store's record for it: the run whose
// run job it is, else the subagent whose job it is; both are nil for a job
// the store does not know.
func lookupJob(store Store, job string) (*RunRecord, *ChildRecord, error) {
	if err := validJobID(job); err != nil {
		return nil, nil, err
	}
	run, err := store.FindRunByJob(job)
	if err != nil || run != nil {
		return run, nil, err
	}
	child, err := store.FindChildByJob(job)
	return nil, child, err
}

// JobLedger returns a job's ledger document (FDR 0019 §1 `juggler
// job-ledger`): a run job's run ledger, or an agent job's result spool.
// target is needed only for a job the store does not know.
func JobLedger(ctx context.Context, deps LifecycleDeps, job, target string) ([]byte, error) {
	run, child, err := lookupJob(deps.Store, job)
	switch {
	case err != nil:
		return nil, err
	case run != nil:
		return os.ReadFile(deps.Store.RunLedgerPath(run.RunKey))
	case child != nil:
		target = child.Parent
	}
	if target == "" {
		return nil, fmt.Errorf("job %s is not a known juggler job; pass --target <channel principal>", job)
	}
	path, err := deps.Ringmaster.SpoolPath(ctx, target, job)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(data) == 0) {
		return nil, fmt.Errorf("job %s has no ledger yet (spool %s is empty)", job, path)
	}
	return data, err
}

// JobHandles returns a job's handle table: the store's record for a known
// juggler job, else the journal mirror on target's channel.
func JobHandles(ctx context.Context, deps LifecycleDeps, job, target string) ([]Holder, error) {
	run, child, err := lookupJob(deps.Store, job)
	switch {
	case err != nil:
		return nil, err
	case run != nil:
		return run.Holders, nil
	case child != nil:
		return child.Holders, nil
	}
	if target == "" {
		return nil, fmt.Errorf("job %s is not a known juggler job; pass --target <channel principal>", job)
	}
	recs, err := deps.Ringmaster.Records(ctx, target, job)
	if err != nil {
		return nil, err
	}
	return HoldersFromRecords(recs), nil
}

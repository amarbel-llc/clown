package jugglerrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ResolveRequest is `juggler resolve`.
type ResolveRequest struct {
	RunJob string
	// State is succeeded, or failed when the fallback ran.
	State  string
	Reason string
	// FallbackArtifacts is the fallback script's own outcome.
	FallbackArtifacts []Artifact
	// StopGrace bounds the wait for each live child to terminalize;
	// DefaultStopGrace when zero.
	StopGrace time.Duration
	// ResultLine, when CanaryRoom is set, is posted into CanaryRoom as the
	// run root before anything else: the root's account dies at teardown.
	ResultLine string
	CanaryRoom string
	// KeepAccounts skips the account teardown (debugging).
	KeepAccounts bool
}

// ResolveOutcome is `juggler resolve`'s stdout object.
type ResolveOutcome struct {
	RunKey string     `json:"run_key"`
	RunJob string     `json:"run_job"`
	State  string     `json:"state"`
	Reason ExitReason `json:"reason"`
	Ledger string     `json:"ledger"`
	Woken  []string   `json:"woken"`
	// AlreadyResolved: the run was resolved before; nothing but a pending
	// teardown was done.
	AlreadyResolved bool `json:"already_resolved"`
	// TornDown: every account of the run is gone (RunRecord.TornDown).
	TornDown bool `json:"torn_down"`
	// Teardown is the teardown entries this call appended to the run ledger.
	Teardown []RunLedgerEntry `json:"teardown,omitempty"`
}

// Resolve is the run's last call (FDR 0019 §1). With a canary room it first
// posts the result line there as the run root (a `canary` ledger note; a
// failed post changes nothing else). It then asks every child job of the run
// that is not yet terminal to cancel and waits up to the stop grace for each
// (a `subagent_stop` ledger entry, stopped_by_resolve, records the outcome);
// appends the `fallback` entry to the run ledger, writes the ledger to the
// store and the run job's spool, writes the run job's terminal record and
// sends the run's exit wake to the run job's holders (the issuer). Last,
// unless KeepAccounts, it tears the run's accounts down (teardownAccounts).
// Every step is retry-safe: the ledger entries are appended once (the
// ledger's resolved field gates them), an existing terminal record is
// relayed, the wake marker prevents a second wake, and resolving a resolved
// run performs only a teardown still pending.
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
		return finishTeardown(ctx, deps, run, req.KeepAccounts, out)
	}

	if req.CanaryRoom != "" {
		if err := postCanaryOnce(ctx, deps, run, req.CanaryRoom, req.ResultLine); err != nil {
			return out, err
		}
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
		stops, err := stopLiveChildren(ctx, deps, run.RunKey, req.StopGrace)
		if err != nil {
			return out, err
		}
		ledger.Calls = append(ledger.Calls, stops...)
		ledger.Calls = append(ledger.Calls, FallbackEntry(req.State, req.Reason, artifacts))
		ledger.Resolved = &Resolution{State: req.State, Reason: req.Reason, At: deps.Now().UTC()}
	}
	resultRef := out.Ledger
	if spool, err := persistLedger(ctx, deps, run, ledger); err != nil {
		return out, err
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
	return finishTeardown(ctx, deps, run, req.KeepAccounts, out)
}

// persistLedger writes the run ledger to the store and, best effort, to the
// run job's result spool, returning the spool path ("" when there is none or
// the spool write failed, which is logged).
func persistLedger(ctx context.Context, deps LifecycleDeps, run *RunRecord, ledger RunLedger) (string, error) {
	if err := writeJSONAtomic(deps.Store.RunLedgerPath(run.RunKey), ledger); err != nil {
		return "", fmt.Errorf("writing the run ledger: %w", err)
	}
	doc, err := marshalDocument(ledger)
	if err != nil {
		return "", err
	}
	spool, err := writeSpool(ctx, deps.Ringmaster, run.Issuer, run.RunJob, doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "juggler: resolve: %v\n", err)
		return "", nil
	}
	return spool, nil
}

// postCanaryOnce posts the result line into the canary room as the run root,
// unless the run's canary marker says an earlier (partial) resolve already
// did. The marker is written right after a successful post, then the canary
// note is appended to the run ledger whatever its resolved state, so a resolve
// retried after any later failure neither posts again nor loses the note. A
// failed post is noted too, writes no marker, and changes nothing else.
func postCanaryOnce(ctx context.Context, deps LifecycleDeps, run *RunRecord, room, line string) error {
	marker := deps.Store.canaryMarkerPath(run.RunKey)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, postErr := deps.Troupe.PostStanza(ctx, run.rootIdentity(), room, ResolveSource, []byte(line))
	if postErr == nil {
		if err := writeFileAtomic(marker, []byte(id+"\n")); err != nil {
			return fmt.Errorf("writing the canary marker (the line was posted as %s): %w", id, err)
		}
	}
	ledger, err := deps.Store.loadRunLedger(run.RunKey)
	if err != nil {
		return err
	}
	ledger.Calls = append(ledger.Calls, CanaryEntry(room, id, postErr))
	if err := writeJSONAtomic(deps.Store.RunLedgerPath(run.RunKey), ledger); err != nil {
		return fmt.Errorf("writing the run ledger: %w", err)
	}
	return nil
}

// finishTeardown runs a resolved run's pending teardown unless keep, and
// reports it in out.
func finishTeardown(ctx context.Context, deps LifecycleDeps, run *RunRecord, keep bool, out ResolveOutcome) (ResolveOutcome, error) {
	if !keep && !run.TornDown {
		entries, err := teardownAccounts(ctx, deps, run)
		out.Teardown = entries
		if err != nil {
			return out, err
		}
	}
	out.TornDown = run.TornDown
	return out, nil
}

// teardownAccounts deletes the run's accounts (FDR 0019 §1): for each child
// whose job is terminal, its room affiliation (a room the run created; best
// effort), its account (`troupe mint-revoke`; a login failure means already
// gone) and its password file; then, once no child account remains, the
// root's own affiliation (only when an operator owner remains to keep the
// room), account and file. A child still running keeps its account (not ok,
// "still running"), and so does the root while any child account remains.
// One `teardown` entry per account handled is appended to the run ledger
// (and the run job's spool); accounts with an ok entry already are skipped,
// so a retry does only what is left. run.TornDown is set and saved when every
// account is gone.
func teardownAccounts(ctx context.Context, deps LifecycleDeps, run *RunRecord) ([]RunLedgerEntry, error) {
	ledger, err := deps.Store.loadRunLedger(run.RunKey)
	if err != nil {
		return nil, err
	}
	gone := map[string]bool{}
	for _, e := range ledger.Calls {
		if e.Tool == TeardownTool && e.OK {
			gone[e.Principal] = true
		}
	}
	children, err := deps.Store.runChildren(run.RunKey)
	if err != nil {
		return nil, err
	}
	root := run.rootIdentity()
	var entries []RunLedgerEntry
	childrenGone := true
	for _, child := range children {
		if gone[child.Principal] {
			continue
		}
		e := teardownChild(ctx, deps, run, root, child)
		entries = append(entries, e)
		childrenGone = childrenGone && e.OK
	}
	rootGone := gone[run.RootPrincipal]
	if !rootGone {
		e := TeardownEntry(run.RootPrincipal, run.RootJID, false, "kept: a child account of the run remains")
		if childrenGone {
			e = teardownRoot(ctx, deps, run, root)
		}
		entries = append(entries, e)
		rootGone = e.OK
	}
	if len(entries) > 0 {
		ledger.Calls = append(ledger.Calls, entries...)
		if _, err := persistLedger(ctx, deps, run, ledger); err != nil {
			return entries, err
		}
	}
	run.TornDown = childrenGone && rootGone
	if err := deps.Store.SaveRun(run); err != nil {
		return entries, fmt.Errorf("saving the run record: %w", err)
	}
	return entries, nil
}

func teardownChild(ctx context.Context, deps LifecycleDeps, run *RunRecord, root Identity, child *ChildRecord) RunLedgerEntry {
	recs, err := deps.Ringmaster.Records(ctx, child.Parent, child.Job)
	if err != nil {
		return TeardownEntry(child.Principal, child.JID, false, fmt.Sprintf("kept: journal unreadable: %v", err))
	}
	if _, terminal := TerminalRecord(recs); !terminal && len(recs) > 0 {
		return TeardownEntry(child.Principal, child.JID, false, "still running")
	}
	var notes []string
	if run.RoomCreated {
		if err := deps.Rooms.Affiliate(ctx, root, run.Room, AffiliationNone, child.JID); err != nil {
			notes = append(notes, fmt.Sprintf("room affiliation not removed: %v", err))
		}
	}
	ok, reason := revokeAccount(ctx, deps, child.Principal, child.CredentialRef)
	return TeardownEntry(child.Principal, child.JID, ok, joinNotes(reason, notes))
}

func teardownRoot(ctx context.Context, deps LifecycleDeps, run *RunRecord, root Identity) RunLedgerEntry {
	var notes []string
	switch {
	case !run.RoomCreated:
		notes = append(notes, "root affiliation kept: the room was not provisioned by juggler")
	case run.OperatorJID == "":
		notes = append(notes, "root affiliation kept: the room has no operator owner")
	default:
		if err := deps.Rooms.Affiliate(ctx, root, run.Room, AffiliationNone, run.RootJID); err != nil {
			notes = append(notes, fmt.Sprintf("root affiliation not removed: %v", err))
		}
	}
	ok, reason := revokeAccount(ctx, deps, run.RootPrincipal, run.RootCredentialRef)
	return TeardownEntry(run.RootPrincipal, run.RootJID, ok, joinNotes(reason, notes))
}

// revokeAccount deletes one minted account and its password file. An absent
// file means the account is already gone (troupe would not contact the
// server); a login failure means the same, as troupe cannot tell a deleted
// account from a wrong password.
func revokeAccount(ctx context.Context, deps LifecycleDeps, principal, passwordFile string) (bool, string) {
	if passwordFile == "" {
		return false, "kept: no credential reference recorded"
	}
	if _, err := os.Stat(passwordFile); errors.Is(err, os.ErrNotExist) {
		return true, "already gone: no password file"
	}
	reason := "revoked"
	if err := deps.Troupe.RevokeMint(ctx, principal, passwordFile); err != nil {
		if !IsLoginFailure(err) {
			return false, fmt.Sprintf("kept: mint-revoke failed: %v", err)
		}
		reason = "already gone: login failed"
	}
	if err := removeIfExists(passwordFile); err != nil {
		return false, fmt.Sprintf("%s, but deleting the password file failed: %v", reason, err)
	}
	return true, reason
}

func joinNotes(reason string, notes []string) string {
	if len(notes) == 0 {
		return reason
	}
	return reason + "; " + strings.Join(notes, "; ")
}

// stopLiveChildren requests cancellation of every child job of the run that is
// not yet terminal and waits up to grace for each (concurrently), so a later
// account teardown never deletes an account under a running agent. It returns
// one ledger entry per child it had to stop.
func stopLiveChildren(ctx context.Context, deps LifecycleDeps, runKey string, grace time.Duration) ([]RunLedgerEntry, error) {
	children, err := deps.Store.runChildren(runKey)
	if err != nil {
		return nil, err
	}
	entries := make([]*RunLedgerEntry, len(children))
	var wg sync.WaitGroup
	for i, child := range children {
		wg.Add(1)
		go func(i int, child *ChildRecord) {
			defer wg.Done()
			recs, err := deps.Ringmaster.Records(ctx, child.Parent, child.Job)
			if err != nil {
				// Resolve gates teardown: an unreadable child journal is
				// recorded, never fatal. The cancel is still tried.
				_ = deps.Ringmaster.Cancel(ctx, child.Parent, child.Job, "run resolved")
				e := ChildStopFailureEntry(child, err)
				entries[i] = &e
				return
			}
			if _, terminal := TerminalRecord(recs); terminal || len(recs) == 0 {
				return
			}
			_, terminalized, err := requestStop(ctx, deps.Ringmaster, child.Parent, child.Job, "run resolved", grace)
			if err != nil {
				e := ChildStopFailureEntry(child, err)
				entries[i] = &e
				return
			}
			e := ChildStopEntry(child, terminalized)
			entries[i] = &e
		}(i, child)
	}
	wg.Wait()
	var out []RunLedgerEntry
	for _, e := range entries {
		if e != nil {
			out = append(out, *e)
		}
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

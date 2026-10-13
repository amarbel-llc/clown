package jugglerrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// DefaultCleanOlderThan guards `juggler clean` against reaping a run whose
// retry (a pending run) or whose resolve-time teardown (a resolved run) may
// still be in flight: a run is reaped only once it has sat in its
// clean-eligible state (Pending, or Resolved) for at least this long.
const DefaultCleanOlderThan = time.Hour

// CleanRequest is `juggler clean` (clown#250): the retention sweep for the
// state a failed `--new-run` and a finished run deliberately leave behind
// (FDR 0019 Limitations). Act performs the reap; false (the CLI default) only
// lists what would be reaped.
type CleanRequest struct {
	// OlderThan guards both categories below; DefaultCleanOlderThan when
	// zero or negative.
	OlderThan time.Duration
	Act       bool
}

// CleanRunResult is one run Clean decided to report: a pending run past
// OlderThan (reaped: its root account, password file and the pending record
// itself) or a resolved run past OlderThan (reaped: its children's staged
// brief/moxyfile files and traffic.jsonl files). A run that is neither — still
// open, or not yet past the guard — is never listed.
type CleanRunResult struct {
	RunKey string `json:"run_key"`
	// Kind is "pending" or "resolved".
	Kind string `json:"kind"`
	Age  string `json:"age"`
	// Reaped is whether the reap (Act) succeeded, or would have (dry-run).
	Reaped bool   `json:"reaped"`
	Detail string `json:"detail"`
	Error  string `json:"error,omitempty"`
}

// CleanOutcome is `juggler clean`'s stdout object.
type CleanOutcome struct {
	DryRun    bool             `json:"dry_run"`
	OlderThan string           `json:"older_than"`
	Runs      []CleanRunResult `json:"runs"`
}

// Clean sweeps the store for the two things clown#250 exists to reap: a
// pending run (`spawn --new-run` failed after minting the root; FDR 0019 §2)
// whose retry never came, and a resolved run's per-child staged files and
// traffic.jsonl, neither of which `resolve` touches (FDR 0019 Limitations:
// resolve tears down accounts, nothing sweeps these). A run that is neither
// pending nor resolved — still open — is never touched. OlderThan guards
// both categories against an in-flight retry (pending) or a resolve whose
// own teardown call raced this one (resolved); it is measured from
// RunRecord.CreatedAt for a pending run and Resolution.At for a resolved
// one.
func Clean(ctx context.Context, deps LifecycleDeps, req CleanRequest) (CleanOutcome, error) {
	deps = deps.withDefaults()
	olderThan := req.OlderThan
	if olderThan <= 0 {
		olderThan = DefaultCleanOlderThan
	}
	runs, err := deps.Store.runs()
	if err != nil {
		return CleanOutcome{}, fmt.Errorf("listing runs: %w", err)
	}
	now := deps.Now()
	out := CleanOutcome{DryRun: !req.Act, OlderThan: olderThan.String(), Runs: []CleanRunResult{}}
	for _, run := range runs {
		switch {
		case run.Pending:
			if age := now.Sub(run.CreatedAt); age >= olderThan {
				out.Runs = append(out.Runs, cleanPendingRun(ctx, deps, run, req.Act, age))
			}
		case run.Resolved != nil:
			if age := now.Sub(run.Resolved.At); age >= olderThan {
				out.Runs = append(out.Runs, cleanResolvedRun(deps, run, req.Act, age))
			}
		}
	}
	return out, nil
}

// cleanPendingRun reaps one pending run past the age guard: the root account
// (teardownRoot — the same helper `juggler resolve` uses, so the
// kept-room-affiliation rule and the account-gone semantics are identical),
// a teardown ledger entry recording it, and, only once the account is
// confirmed gone, the pending record itself.
//
// The ledger entry is appended directly (loadRunLedger, then
// writeJSONAtomic), not through the public AppendRunLedgerEntry: that guard
// deliberately refuses a pending run's ledger (RunRecord.CheckOpen), since
// nothing else should touch it before a retry resumes the run. clean is the
// one caller meant to bypass that guard — it is reaping the run, not
// resuming it. A record left in place (the account could not be confirmed
// gone) is retried by a later clean, the same as resolve's own teardown
// retries a still-pending account.
func cleanPendingRun(ctx context.Context, deps LifecycleDeps, run *RunRecord, act bool, age time.Duration) CleanRunResult {
	res := CleanRunResult{RunKey: run.RunKey, Kind: "pending", Age: age.Round(time.Second).String()}
	if !act {
		res.Detail = "would revoke the root account and remove the pending record"
		return res
	}
	unlock, err := lock(deps.Store.runPath(run.RunKey) + ".lock")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer unlock()
	fresh, err := deps.Store.LoadRun(run.RunKey)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if fresh == nil || !fresh.Pending {
		res.Detail = "skipped: no longer pending (retried, or already cleaned, since the sweep listed it)"
		return res
	}
	entry := teardownRoot(ctx, deps, fresh, fresh.rootIdentity())
	ledger, err := deps.Store.loadRunLedger(fresh.RunKey)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	ledger.Calls = append(ledger.Calls, entry)
	if err := writeJSONAtomic(deps.Store.RunLedgerPath(fresh.RunKey), ledger); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Reaped, res.Detail = entry.OK, entry.Reason
	if entry.OK {
		if err := os.Remove(deps.Store.runPath(fresh.RunKey)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			res.Error = fmt.Sprintf("account reaped but removing the pending record failed: %v", err)
			return res
		}
		res.Detail += "; pending record removed"
	}
	return res
}

// cleanResolvedRun sweeps one resolved run's per-child staged files
// (<digest>.brief.toml, .moxyfile.toml, .parent-moxyfile.toml — scratch that
// outlives the spawn it staged for) and traffic.jsonl files (clown#255; as
// sensitive as the tool arguments they record, in full). Neither touches an
// account: resolve already tore those down (or deliberately kept them,
// --keep-accounts), and clean's job here is retention of leftover files, not
// teardown. Best-effort per file: one failed removal is recorded in the
// result, never fatal to the rest of the sweep.
func cleanResolvedRun(deps LifecycleDeps, run *RunRecord, act bool, age time.Duration) CleanRunResult {
	res := CleanRunResult{RunKey: run.RunKey, Kind: "resolved", Age: age.Round(time.Second).String()}
	children, err := deps.Store.runChildren(run.RunKey)
	if err != nil {
		res.Error = fmt.Sprintf("listing children: %v", err)
		return res
	}
	var paths []string
	for _, child := range children {
		paths = append(paths,
			deps.Store.stagedBriefPath(run.RunKey, child.BriefDigest),
			deps.Store.stagedChildMoxyfilePath(run.RunKey, child.BriefDigest),
			deps.Store.stagedParentMoxyfilePath(run.RunKey, child.BriefDigest),
			deps.Store.TrafficPath(child.Principal, child.Job),
		)
	}
	var found, removed int
	var removeErrs []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		found++
		if !act {
			continue
		}
		if err := removeIfExists(p); err != nil {
			removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		removed++
	}
	switch {
	case !act:
		res.Detail = fmt.Sprintf("would remove %d staged/traffic file(s)", found)
	default:
		res.Reaped = found == 0 || removed == found
		res.Detail = fmt.Sprintf("removed %d of %d staged/traffic file(s)", removed, found)
	}
	if len(removeErrs) > 0 {
		res.Error = joinNotes(removeErrs[0], removeErrs[1:])
	}
	return res
}

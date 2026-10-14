package jugglerrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultCleanOlderThan is the CLI's default --older-than: it guards
// `juggler clean` against reaping a pending run whose retry may still be in
// flight. Not applied by Clean itself — req.OlderThan is used exactly as
// given, including a caller's deliberate zero ("no guard: reap any pending
// run, however young") — only cmd/juggler's flag default falls back to it.
const DefaultCleanOlderThan = time.Hour

// DefaultResolvedOlderThan is the CLI's default --resolved-older-than: unlike
// OlderThan (a retry guard), this is a retention policy on a resolved run's
// staged and traffic.jsonl files, which are audit material, not scratch that
// should vanish quickly — 30 days matches the host's current interim
// retention rule. Not applied by Clean itself, for the same reason
// DefaultCleanOlderThan isn't.
const DefaultResolvedOlderThan = 30 * 24 * time.Hour

// CleanRequest is `juggler clean` (clown#250): the retention sweep for the
// state a failed `--new-run` and a finished run deliberately leave behind
// (FDR 0019 Limitations). Act performs the reap; false (the CLI default) only
// lists what would be reaped.
type CleanRequest struct {
	// OlderThan guards the pending-run sweep, used exactly as given (see
	// DefaultCleanOlderThan's doc on why Clean applies no fallback here).
	OlderThan time.Duration
	// Resolved opts into the resolved-run sweep (off by default: it is a
	// retention policy, not a retry guard, and not every caller wants it on
	// every sweep). ResolvedOlderThan is used exactly as given, same as
	// OlderThan, and is only consulted when Resolved is set.
	Resolved          bool
	ResolvedOlderThan time.Duration
	Act               bool
}

// CleanRunResult is one run Clean decided to report: a pending run past
// OlderThan (reaped: its root account and password file, tombstoning the
// record) or, with Resolved set, a resolved run past ResolvedOlderThan
// (reaped: its terminal children's staged brief/moxyfile files and
// traffic.jsonl files). A run that is neither — still open, or not yet past
// its guard — is never listed, and nor is a resolved run with nothing left to
// report (clown#250 review point 6).
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
	DryRun            bool             `json:"dry_run"`
	OlderThan         string           `json:"older_than"`
	Resolved          bool             `json:"resolved"`
	ResolvedOlderThan string           `json:"resolved_older_than,omitempty"`
	Runs              []CleanRunResult `json:"runs"`
}

// Clean sweeps the store for the things clown#250 exists to reap: a pending
// run (`spawn --new-run` failed after minting the root; FDR 0019 §2) whose
// retry never came, and, with Resolved set, a resolved run's per-child staged
// files and traffic.jsonl, neither of which `resolve` touches (FDR 0019
// Limitations: resolve tears down accounts, nothing sweeps these). A run that
// is neither pending nor resolved — still open — is never touched. OlderThan
// and ResolvedOlderThan each guard their own category: OlderThan against an
// in-flight retry (measured from RunRecord.PendingSince, which a failed
// --new-run stamps fresh on every attempt — not CreatedAt, which a retry
// carries forward unchanged); ResolvedOlderThan is a retention policy
// measured from Resolution.At, not a retry guard.
func Clean(ctx context.Context, deps LifecycleDeps, req CleanRequest) (CleanOutcome, error) {
	deps = deps.withDefaults()
	runs, err := deps.Store.runs()
	if err != nil {
		return CleanOutcome{}, fmt.Errorf("listing runs: %w", err)
	}
	now := deps.Now()
	out := CleanOutcome{DryRun: !req.Act, OlderThan: req.OlderThan.String(), Resolved: req.Resolved, Runs: []CleanRunResult{}}
	if req.Resolved {
		out.ResolvedOlderThan = req.ResolvedOlderThan.String()
	}
	for _, run := range runs {
		switch {
		case run.Pending:
			if age := now.Sub(run.PendingSince); age >= req.OlderThan {
				out.Runs = append(out.Runs, cleanPendingRun(ctx, deps, run, req.Act, age))
			}
		case req.Resolved && run.Resolved != nil:
			if age := now.Sub(run.Resolved.At); age >= req.ResolvedOlderThan {
				if res := cleanResolvedRun(ctx, deps, run, req.Act, age); res != nil {
					out.Runs = append(out.Runs, *res)
				}
			}
		}
	}
	return out, nil
}

// cleanPendingRun reaps one pending run past the age guard: the root account
// (teardownRoot — the same helper `juggler resolve` uses, so the
// kept-room-affiliation rule and the account-gone semantics are identical),
// a teardown ledger entry recording it, and, only once the account is
// confirmed gone, tombstoning the record (RunRecord.Reaped; clown#250
// review). A tombstone, not a delete: the root principal is deterministic
// (NewRootPrincipal(RunKey)), so deleting the record would let a later
// `spawn --new-run` with the same key re-mint a brand-new account under that
// same principal — one the ledger's existing ok `teardown` entry already
// marks gone, so resolve's teardownAccounts would skip revoking the NEW
// account and it would leak forever. NewRun refuses a reaped key instead
// (RunRecord.CheckOpen), so this can never happen.
//
// The ledger entry is appended directly (loadRunLedger, then
// writeJSONAtomic), not through the public AppendRunLedgerEntry: that guard
// deliberately refuses a pending (or reaped) run's ledger (RunRecord.
// CheckOpen), since nothing else should touch it before a retry resumes the
// run. clean is the one caller meant to bypass that guard — it is reaping
// the run, not resuming it. A record left in place (the account could not be
// confirmed gone) is retried by a later clean, the same as resolve's own
// teardown retries a still-pending account.
func cleanPendingRun(ctx context.Context, deps LifecycleDeps, run *RunRecord, act bool, age time.Duration) CleanRunResult {
	res := CleanRunResult{RunKey: run.RunKey, Kind: "pending", Age: age.Round(time.Second).String()}
	if !act {
		res.Detail = "would revoke the root account and tombstone the run key"
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
		reapedAt := deps.Now().UTC()
		fresh.Pending, fresh.Reaped, fresh.ReapedAt = false, true, &reapedAt
		if err := deps.Store.SaveRun(fresh); err != nil {
			res.Error = fmt.Sprintf("account reaped but tombstoning the record failed: %v", err)
			return res
		}
		res.Detail += "; run key tombstoned (retired, never resumable)"
	}
	return res
}

// stillRunning reports whether child's job is not yet terminal — the same
// Records/TerminalRecord check teardownChild uses to keep a live child's
// account at resolve-time. A resolved run can still hold a live child
// (resolve's own stop grace timed out, or --keep-accounts), and clean must
// never unlink a traffic.jsonl a live agent is still appending to. A
// Records error, or zero records (no journal at all, e.g. a hand-built
// test fixture), is treated as "don't know, sweep anyway" — the same as
// teardownChild treats zero records.
func stillRunning(ctx context.Context, deps LifecycleDeps, child *ChildRecord) bool {
	recs, err := deps.Ringmaster.Records(ctx, child.Parent, child.Job)
	if err != nil {
		return false
	}
	_, terminal := TerminalRecord(recs)
	return !terminal && len(recs) > 0
}

// cleanResolvedRun sweeps one resolved run's per-TERMINAL-child staged files
// (<digest>.brief.toml, .moxyfile.toml, .parent-moxyfile.toml — scratch that
// outlives the spawn it staged for) and traffic.jsonl files (clown#255; as
// sensitive as the tool arguments they record, in full), located via each
// child record's own (Principal, Job) pair — TrafficPath is keyed by both,
// not just the run, since one run can hold many children's traffic files.
// A still-running child (stillRunning) is skipped entirely: its files are
// still live. Neither touches an account: resolve already tore those down
// (or deliberately kept them, --keep-accounts), and clean's job here is
// retention of leftover files, not teardown. Best-effort per file: one
// failed removal is recorded in the result, never fatal to the rest of the
// sweep. Returns nil when there is nothing to report (every file already
// gone or never existed, and no error) — clown#250 review point 6: a
// periodic sweep's output must not keep listing a run with nothing left.
func cleanResolvedRun(ctx context.Context, deps LifecycleDeps, run *RunRecord, act bool, age time.Duration) *CleanRunResult {
	res := CleanRunResult{RunKey: run.RunKey, Kind: "resolved", Age: age.Round(time.Second).String()}
	children, err := deps.Store.runChildren(run.RunKey)
	if err != nil {
		res.Error = fmt.Sprintf("listing children: %v", err)
		return &res
	}
	var found, removed, liveSkipped int
	var removeErrs []string
	sweep := func(p string) {
		if p == "" {
			return
		}
		if _, err := os.Stat(p); err != nil {
			return
		}
		found++
		if !act {
			return
		}
		if err := removeIfExists(p); err != nil {
			removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", p, err))
			return
		}
		removed++
	}
	for _, child := range children {
		if stillRunning(ctx, deps, child) {
			liveSkipped++
			continue
		}
		sweep(deps.Store.stagedBriefPath(run.RunKey, child.BriefDigest))
		sweep(deps.Store.stagedChildMoxyfilePath(run.RunKey, child.BriefDigest))
		sweep(deps.Store.stagedParentMoxyfilePath(run.RunKey, child.BriefDigest))
		traffic := deps.Store.TrafficPath(child.Principal, child.Job)
		beforeRemoved := removed
		sweep(traffic)
		if act && removed > beforeRemoved {
			// Best-effort: drop the now-possibly-empty traffic/<principal>/
			// directory. Fails silently (os.Remove on a non-empty or
			// already-gone directory) when another job's traffic file still
			// lives there, or a concurrent sweep got to it first.
			_ = os.Remove(filepath.Dir(traffic))
		}
	}
	switch {
	case !act:
		res.Detail = fmt.Sprintf("would remove %d staged/traffic file(s)", found)
	default:
		res.Reaped = removed == found
		res.Detail = fmt.Sprintf("removed %d of %d staged/traffic file(s)", removed, found)
	}
	if liveSkipped > 0 {
		res.Detail = fmt.Sprintf("%s (%d still-running child skipped)", res.Detail, liveSkipped)
	}
	if len(removeErrs) > 0 {
		res.Error = joinNotes(removeErrs[0], removeErrs[1:])
	}
	if found == 0 && res.Error == "" {
		return nil
	}
	return &res
}

package jugglerrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pendingRun creates a pending run (NewRun fails after the root mint,
// clown#250's target) at createdAt, with testRoot as its root principal.
func pendingRun(t *testing.T, h *harness, key string, createdAt time.Time) *RunRecord {
	t.Helper()
	pinPrincipals(h, testRoot)
	h.fakes.Fail(t, "ringmaster", "start")
	h.deps.Now = func() time.Time { return createdAt }
	if _, err := NewRun(context.Background(), h.deps, NewRunRequest{RunKey: key, Issuer: testIssuer, Input: []byte("x"), Room: "run@rooms.test"}); err == nil {
		t.Fatal("want a PendingRunError")
	}
	rec, err := h.deps.Store.LoadRun(key)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || !rec.Pending {
		t.Fatalf("setup failed to produce a pending run: %+v", rec)
	}
	return rec
}

func TestClean_DryRunListsPendingPastThresholdWithoutTouchingIt(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pending := pendingRun(t, h, "k", t0)
	h.deps.Now = func() time.Time { return t0.Add(2 * time.Hour) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: false})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if !out.DryRun || len(out.Runs) != 1 {
		t.Fatalf("out = %+v", out)
	}
	r := out.Runs[0]
	if r.RunKey != "k" || r.Kind != "pending" || r.Reaped {
		t.Errorf("result = %+v", r)
	}
	if len(h.fakes.Revoked(t)) != 0 {
		t.Error("dry-run must not call mint-revoke")
	}
	if _, err := os.Stat(pending.RootCredentialRef); err != nil {
		t.Errorf("dry-run must not delete the password file: %v", err)
	}
	if rec, _ := h.deps.Store.LoadRun("k"); rec == nil || !rec.Pending || rec.Reaped {
		t.Errorf("dry-run must not touch the pending record: %+v", rec)
	}
}

func TestClean_SkipsPendingYoungerThanThreshold(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pendingRun(t, h, "k", t0)
	h.deps.Now = func() time.Time { return t0.Add(10 * time.Minute) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 0 {
		t.Fatalf("a run younger than OlderThan must not be listed: %+v", out.Runs)
	}
	if len(h.fakes.Revoked(t)) != 0 {
		t.Error("must not have touched the account")
	}
}

// TestClean_ZeroOlderThanReapsImmediately is the clown#250 review's blocking
// regression test: Clean must use OlderThan exactly as given, including an
// explicit zero, rather than silently falling back to DefaultCleanOlderThan
// (the bug caught only after ebd2f97 was already sent for review — the fix
// was never committed into that sha).
func TestClean_ZeroOlderThanReapsImmediately(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pendingRun(t, h, "k", t0)
	h.deps.Now = func() time.Time { return t0.Add(time.Second) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: 0, Act: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 1 || !out.Runs[0].Reaped {
		t.Fatalf("a 1s-old pending run with OlderThan=0 must be reaped immediately: %+v", out.Runs)
	}
}

func TestClean_ActReapsPendingPastThreshold(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pending := pendingRun(t, h, "k", t0)
	h.deps.Now = func() time.Time { return t0.Add(2 * time.Hour) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 1 {
		t.Fatalf("out = %+v", out)
	}
	r := out.Runs[0]
	if !r.Reaped || r.Error != "" || !strings.Contains(r.Detail, "revoked") || !strings.Contains(r.Detail, "tombstoned") {
		t.Errorf("result = %+v", r)
	}
	if revoked := h.fakes.Revoked(t); len(revoked) != 1 || revoked[0] != testRoot {
		t.Errorf("revoked = %v, want [%s]", revoked, testRoot)
	}
	if _, err := os.Stat(pending.RootCredentialRef); !os.IsNotExist(err) {
		t.Errorf("password file must be deleted: %v", err)
	}
	rec, err := h.deps.Store.LoadRun("k")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.Pending || !rec.Reaped || rec.ReapedAt == nil {
		t.Errorf("the record must be tombstoned (kept, Pending=false, Reaped=true), not removed: %+v", rec)
	}
	ledger, err := h.deps.Store.loadRunLedger("k")
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Calls) != 1 || ledger.Calls[0].Tool != TeardownTool || ledger.Calls[0].Principal != testRoot || !ledger.Calls[0].OK {
		t.Errorf("ledger.Calls = %+v, want one ok teardown entry for the root", ledger.Calls)
	}
}

// TestClean_ReapedKeyRefusesNewRun is the clown#250 review's second blocking
// test: a tombstoned run key must never be resumed. The root principal is
// deterministic (NewRootPrincipal(RunKey)), so resuming would re-mint a
// brand-new account under the same principal the ledger's teardown entry
// already marks gone — resolve's teardownAccounts would then skip revoking
// the NEW account and it would leak forever.
func TestClean_ReapedKeyRefusesNewRun(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pendingRun(t, h, "k", t0)
	h.deps.Now = func() time.Time { return t0.Add(2 * time.Hour) }
	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: true})
	if err != nil || len(out.Runs) != 1 || !out.Runs[0].Reaped {
		t.Fatalf("setup: reap failed: %+v, %v", out, err)
	}
	minted := len(h.fakes.Calls(t, "troupe"))

	_, err = NewRun(context.Background(), h.deps, NewRunRequest{RunKey: "k", Issuer: testIssuer, Input: []byte("x"), Room: "run@rooms.test"})
	if err == nil || !strings.Contains(err.Error(), "reaped") {
		t.Fatalf("NewRun on a reaped key must refuse, naming the tombstone: %v", err)
	}
	if n := len(h.fakes.Calls(t, "troupe")); n != minted {
		t.Errorf("a refused NewRun must mint nothing: troupe calls %d became %d", minted, n)
	}
}

func TestClean_ActKeepsRecordWhenRevokeFails(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pendingRun(t, h, "k", t0)
	h.fakes.Fail(t, "troupe", "mint-revoke")
	h.deps.Now = func() time.Time { return t0.Add(2 * time.Hour) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 1 {
		t.Fatalf("out = %+v", out)
	}
	r := out.Runs[0]
	if r.Reaped || !strings.Contains(r.Detail, "kept") {
		t.Errorf("result = %+v, want a kept (not reaped) outcome", r)
	}
	rec, _ := h.deps.Store.LoadRun("k")
	if rec == nil || !rec.Pending || rec.Reaped {
		t.Errorf("the pending record must remain for a later retry: %+v", rec)
	}
	ledger, err := h.deps.Store.loadRunLedger("k")
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Calls) != 1 || ledger.Calls[0].OK {
		t.Errorf("ledger.Calls = %+v, want one not-ok teardown entry", ledger.Calls)
	}
}

// TestClean_PendingSinceResetsOnEachRetryFailure covers the clown#250 review's
// should-fix 3: the age guard must read PendingSince (stamped fresh by
// failed() on every failed attempt), not CreatedAt (which a resume carries
// forward unchanged) — otherwise a retry policy whose total span exceeds
// OlderThan gets its run reaped between attempts.
func TestClean_PendingSinceResetsOnEachRetryFailure(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pendingRun(t, h, "k", t0)

	retryAt := t0.Add(90 * time.Minute)
	h.deps.Now = func() time.Time { return retryAt }
	if _, err := NewRun(context.Background(), h.deps, NewRunRequest{RunKey: "k", Issuer: testIssuer, Input: []byte("x"), Room: "run@rooms.test"}); err == nil {
		t.Fatal("want the retry's own PendingRunError (ringmaster start is still forced to fail)")
	}

	// 149 minutes after the ORIGINAL failure, but only 59 after the retry's
	// own failure: must NOT be eligible.
	h.deps.Now = func() time.Time { return retryAt.Add(59 * time.Minute) }
	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: false})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 0 {
		t.Fatalf("a CreatedAt-based guard would wrongly reap this; PendingSince must gate it instead: %+v", out.Runs)
	}

	// 61 minutes after the retry's own failure: now eligible.
	h.deps.Now = func() time.Time { return retryAt.Add(61 * time.Minute) }
	out, err = Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: false})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 1 {
		t.Fatalf("61 min past the retry's own failure must be eligible: %+v", out.Runs)
	}
}

// TestClean_NeverTouchesAnOpenRun covers a run that is neither pending nor
// resolved (its spawn --new-run succeeded and it is still in progress):
// Clean must never list or act on it, however old OlderThan.
func TestClean_NeverTouchesAnOpenRun(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.deps.Now = func() time.Time { return t0 }
	pinPrincipals(h, testRoot)
	res := newRun(t, h, "open-run")
	if res.Resolved {
		t.Fatal("setup: run must be open, not resolved")
	}
	h.deps.Now = func() time.Time { return t0.Add(365 * 24 * time.Hour) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Resolved: true, ResolvedOlderThan: time.Hour, Act: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 0 {
		t.Fatalf("an open run must never be listed: %+v", out.Runs)
	}
	if len(h.fakes.Revoked(t)) != 0 {
		t.Error("must not have touched any account")
	}
}

// TestClean_ResolvedSweepIsOptedIntoByFlag covers the clown#250 review's
// should-fix 4b: the resolved-run sweep is off unless Resolved is set, even
// when a resolved run sits well past ResolvedOlderThan.
func TestClean_ResolvedSweepIsOptedIntoByFlag(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := &RunRecord{Schema: RecordSchema, RunKey: "k", RootPrincipal: "root-x", CreatedAt: t0, Resolved: &Resolution{State: StateSucceeded, Reason: "done", At: t0}}
	if err := h.deps.Store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	child := &ChildRecord{Schema: RecordSchema, RunKey: "k", BriefDigest: "sha256:abc", Principal: "child-1", Job: "job-1", Parent: "root-x", CreatedAt: t0}
	if err := h.deps.Store.SaveChild(child); err != nil {
		t.Fatal(err)
	}
	p := h.deps.Store.TrafficPath(child.Principal, child.Job)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.deps.Now = func() time.Time { return t0.Add(365 * 24 * time.Hour) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 0 {
		t.Fatalf("Resolved is unset; the resolved-run sweep must not run: %+v", out.Runs)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("the traffic file must be kept when Resolved is unset: %v", err)
	}
}

// TestClean_SweepsResolvedRunStagedAndTrafficFiles covers the other half of
// clown#250: a resolved run's per-child staged files and traffic.jsonl, left
// behind by spawn and the agent run respectively (FDR 0019 Limitations), are
// swept by age — not the run or child record themselves (the audit trail),
// and not any account (resolve already handled those).
func TestClean_SweepsResolvedRunStagedAndTrafficFiles(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := &RunRecord{Schema: RecordSchema, RunKey: "k", RootPrincipal: "root-x", CreatedAt: t0, Resolved: &Resolution{State: StateSucceeded, Reason: "done", At: t0}}
	if err := h.deps.Store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	child := &ChildRecord{Schema: RecordSchema, RunKey: "k", BriefDigest: "sha256:abc", Principal: "child-1", Job: "job-1", Parent: "root-x", CreatedAt: t0}
	if err := h.deps.Store.SaveChild(child); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		h.deps.Store.stagedBriefPath("k", child.BriefDigest),
		h.deps.Store.stagedChildMoxyfilePath("k", child.BriefDigest),
		h.deps.Store.stagedParentMoxyfilePath("k", child.BriefDigest),
		h.deps.Store.TrafficPath(child.Principal, child.Job),
	}
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.deps.Now = func() time.Time { return t0.Add(2 * time.Hour) }
	req := func(act bool) CleanRequest {
		return CleanRequest{OlderThan: time.Hour, Resolved: true, ResolvedOlderThan: time.Hour, Act: act}
	}

	dry, err := Clean(context.Background(), h.deps, req(false))
	if err != nil {
		t.Fatalf("dry-run Clean: %v", err)
	}
	if len(dry.Runs) != 1 || dry.Runs[0].Kind != "resolved" || dry.Runs[0].Reaped || !strings.Contains(dry.Runs[0].Detail, "would remove 4") {
		t.Fatalf("dry-run result = %+v", dry.Runs)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("dry-run must not remove %s: %v", p, err)
		}
	}

	out, err := Clean(context.Background(), h.deps, req(true))
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 1 {
		t.Fatalf("out = %+v", out)
	}
	r := out.Runs[0]
	if r.Kind != "resolved" || !r.Reaped || !strings.Contains(r.Detail, "removed 4 of 4") {
		t.Errorf("result = %+v", r)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should be removed: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(h.deps.Store.TrafficPath(child.Principal, child.Job))); !os.IsNotExist(err) {
		t.Errorf("the now-empty traffic/<principal>/ directory should be removed too: %v", err)
	}
	if rec, _ := h.deps.Store.LoadRun("k"); rec == nil {
		t.Error("the run record is the audit trail; must not be removed")
	}
	if _, err := os.Stat(h.deps.Store.childPath("k", child.BriefDigest)); err != nil {
		t.Errorf("the child record is the audit trail; must not be removed: %v", err)
	}
	if revoked := h.fakes.Revoked(t); len(revoked) != 0 {
		t.Errorf("a resolved run's sweep must never touch an account: revoked %v", revoked)
	}

	// Idempotent, and nothing left to report (clown#250 review point 6): a
	// second sweep must not keep re-listing a run with nothing left.
	again, err := Clean(context.Background(), h.deps, req(true))
	if err != nil {
		t.Fatalf("second Clean: %v", err)
	}
	if len(again.Runs) != 0 {
		t.Errorf("a fully-swept run must be omitted from a later sweep's output: %+v", again.Runs)
	}
}

// TestClean_ResolvedSweepSkipsAStillRunningChild covers the clown#250 review's
// should-fix 4a: a resolved run can still hold a live child (resolve's own
// stop grace timed out, or --keep-accounts); clean must never unlink a
// traffic.jsonl a live agent is still appending to.
func TestClean_ResolvedSweepSkipsAStillRunningChild(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.deps.Now = func() time.Time { return t0 }
	pinPrincipals(h, testRoot, "child-1")
	res := newProvisionedRun(t, h, "rec-42")
	live := spawnChild(t, h)

	done := &ChildRecord{Schema: RecordSchema, RunKey: res.RunKey, BriefDigest: "sha256:def", Principal: "child-done", Job: "job-done", Parent: testRoot, CreatedAt: t0}
	if err := h.deps.Store.SaveChild(done); err != nil {
		t.Fatal(err)
	}
	livePath := h.deps.Store.TrafficPath(live.Principal, live.Job)
	donePath := h.deps.Store.TrafficPath(done.Principal, done.Job)
	for _, p := range []string{livePath, donePath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateFailed, Reason: "x", StopGrace: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	h.deps.Now = func() time.Time { return t0.Add(31 * 24 * time.Hour) }

	out, err := Clean(context.Background(), h.deps, CleanRequest{Resolved: true, ResolvedOlderThan: 30 * 24 * time.Hour, Act: true})
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(out.Runs) != 1 || !strings.Contains(out.Runs[0].Detail, "still-running child skipped") {
		t.Fatalf("out.Runs = %+v", out.Runs)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Errorf("the still-running child's traffic file must be kept: %v", err)
	}
	if _, err := os.Stat(donePath); !os.IsNotExist(err) {
		t.Errorf("the terminal child's traffic file must be swept: %v", err)
	}
}

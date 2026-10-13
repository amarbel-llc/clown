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
	if rec, _ := h.deps.Store.LoadRun("k"); rec == nil || !rec.Pending {
		t.Errorf("dry-run must not remove the pending record: %+v", rec)
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
	if !r.Reaped || r.Error != "" || !strings.Contains(r.Detail, "revoked") || !strings.Contains(r.Detail, "pending record removed") {
		t.Errorf("result = %+v", r)
	}
	if revoked := h.fakes.Revoked(t); len(revoked) != 1 || revoked[0] != testRoot {
		t.Errorf("revoked = %v, want [%s]", revoked, testRoot)
	}
	if _, err := os.Stat(pending.RootCredentialRef); !os.IsNotExist(err) {
		t.Errorf("password file must be deleted: %v", err)
	}
	if rec, _ := h.deps.Store.LoadRun("k"); rec != nil {
		t.Errorf("pending record must be removed: %+v", rec)
	}
	ledger, err := h.deps.Store.loadRunLedger("k")
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Calls) != 1 || ledger.Calls[0].Tool != TeardownTool || ledger.Calls[0].Principal != testRoot || !ledger.Calls[0].OK {
		t.Errorf("ledger.Calls = %+v, want one ok teardown entry for the root", ledger.Calls)
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
	if rec == nil || !rec.Pending {
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

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: true})
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
	child := &ChildRecord{Schema: RecordSchema, RunKey: "k", BriefDigest: "sha256:abc", Principal: "child-1", Job: "job-1", CreatedAt: t0}
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

	dry, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: false})
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

	out, err := Clean(context.Background(), h.deps, CleanRequest{OlderThan: time.Hour, Act: true})
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
	if rec, _ := h.deps.Store.LoadRun("k"); rec == nil {
		t.Error("the run record is the audit trail; must not be removed")
	}
	if _, err := os.Stat(h.deps.Store.childPath("k", child.BriefDigest)); err != nil {
		t.Errorf("the child record is the audit trail; must not be removed: %v", err)
	}
	if revoked := h.fakes.Revoked(t); len(revoked) != 0 {
		t.Errorf("a resolved run's sweep must never touch an account: revoked %v", revoked)
	}
}

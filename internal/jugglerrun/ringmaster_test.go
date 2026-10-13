package jugglerrun

import (
	"context"
	"errors"
	"strings"
	"testing"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

func TestCheckRingmasterProtocol_MatchesByDefault(t *testing.T) {
	h := newHarness(t)
	if err := CheckRingmasterProtocol(context.Background(), h.fakes.Ringmaster); err != nil {
		t.Errorf("err = %v, want the fake's default protocol to match jobwake.ProtocolVersion (%d)", err, jobwake.ProtocolVersion)
	}
}

func TestCheckRingmasterProtocol_MismatchRefuses(t *testing.T) {
	h := newHarness(t)
	h.fakes.SetRingmasterProtocolVersion(t, "2")
	err := CheckRingmasterProtocol(context.Background(), h.fakes.Ringmaster)
	if err == nil || !strings.Contains(err.Error(), "protocol 2") || !strings.Contains(err.Error(), "protocol 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckRingmasterProtocol_UnparseableOutputRefuses(t *testing.T) {
	h := newHarness(t)
	h.fakes.SetRingmasterProtocolVersion(t, "not-a-number")
	if err := CheckRingmasterProtocol(context.Background(), h.fakes.Ringmaster); err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckRingmasterProtocol_MissingBinaryRefuses(t *testing.T) {
	if err := CheckRingmasterProtocol(context.Background(), "/nonexistent/ringmaster"); err == nil {
		t.Fatal("want an error when the binary cannot be run at all")
	}
}

// TestJobwakeAcquireJobLock_AliveWhileHeldGoneAfterRelease is clown#256's
// finding 6 lock-identity test, at the jobwake level rather than through
// `ringmaster status`'s liveness field: a bats test driving a real
// subprocess `juggler run` against the real ringmaster binary while polling
// `ringmaster status --json` would need three new pieces of harness (a
// background-run helper, a bash-reachable model/tool-plane HTTP fixture, and
// the poll loop itself) that do not exist in zz-tests_bats/juggler_lifecycle.bats
// today — this test instead calls the REAL jobwake.AcquireJobLock directly
// (the same function cmd/juggler's `juggler run` wires). It proves the lock
// file itself enforces mutual exclusion the way a monitor's liveness probe
// depends on: a lock a second AcquireJobLock cannot take is "alive", one it
// can take again is "gone". (Soften: the probe itself — probeJobLock — is a
// separate, instantaneous flock-then-unlock, not a second AcquireJobLock;
// this test exercises the lock file's semantics the probe relies on, not the
// probe's own code path. Identity with what `ringmaster status` reads holds
// by code reading, not by this test driving the real CLI: a.target =
// b.Parent = the CLI's --target, and AcquireJobLock resolves
// ChannelID(resolveSession(target)) the same way Start does.)
func TestJobwakeAcquireJobLock_AliveWhileHeldGoneAfterRelease(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := newHarness(t)
	ctx := context.Background()
	job, err := h.deps.Ringmaster.Start(ctx, testRoot, "test", "test")
	if err != nil {
		t.Fatal(err)
	}

	release, err := jobwake.AcquireJobLock(testRoot, job)
	if err != nil {
		t.Fatalf("AcquireJobLock: %v", err)
	}
	if _, err := jobwake.AcquireJobLock(testRoot, job); !errors.Is(err, jobwake.ErrAlreadyLocked) {
		t.Fatalf("second acquire while the first is held: err = %v, want jobwake.ErrAlreadyLocked (alive)", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	release2, err := jobwake.AcquireJobLock(testRoot, job)
	if err != nil {
		t.Fatalf("acquire after release: err = %v, want success (gone)", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("release2: %v", err)
	}
}

// TestProtocolCheckedRingmaster_IgnoresACancelledCallerContext: the FIRST
// call through a ProtocolCheckedRingmaster instance runs the once-only check
// under its own context.Background()-derived timeout, never the caller's
// ctx (clown#256 review finding 2). Without that, a caller's ctx that is
// already cancelled when the first call lands — WaitCancelRequested's own
// ctx is exactly this, deliberately cancelled by a SIGTERM or a holder's
// cancel — would latch context.Canceled into checkErr via sync.Once,
// failing every later call sharing this instance, including finish's Done
// under a fresh ctx, and losing the agent's own terminal record.
func TestProtocolCheckedRingmaster_IgnoresACancelledCallerContext(t *testing.T) {
	h := newHarness(t)
	pc := &ProtocolCheckedRingmaster{Bin: h.fakes.Ringmaster}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pc.check(cancelledCtx); err != nil {
		t.Fatalf("err = %v, want the protocol check to ignore the caller's own (already cancelled) ctx entirely", err)
	}
}

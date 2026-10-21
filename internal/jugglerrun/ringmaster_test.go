package jugglerrun

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	if err == nil || !strings.Contains(err.Error(), "protocol 2") || !strings.Contains(err.Error(), fmt.Sprintf("protocol %d", jobwake.ProtocolVersion)) {
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
// (the same function cmd/juggler's `juggler run` wires) against ringmaster's
// in-process host service (RFC-0020 §7.3, the store is the service's alone).
// It proves the hold enforces mutual exclusion the way liveness depends on: a
// hold a second AcquireJobLock cannot take is "alive"; once the terminal is
// written and the hold released the job cannot be held again. The real bats
// version (real ringmaster binary, a real `juggler run` subprocess, polling
// `ringmaster status`) goes with the reaper test, tracked as clown#257.
func TestJobwakeAcquireJobLock_AliveWhileHeldGoneAfterRelease(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rt, err := os.MkdirTemp("", "rm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	stopService, err := jobwake.StartService(nil)
	if err != nil {
		t.Fatalf("in-process ringmaster service: %v", err)
	}
	t.Cleanup(stopService)
	job, err := jobwake.Start(jobwake.StartOpts{Target: testRoot, Label: "test", Source: "test"})
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
	// The holder releases only once its terminal record is durable (RFC-0020
	// §5.4); a job settled and released can no longer be held (gone).
	if err := jobwake.Done(testRoot, job, jobwake.TypeSucceeded, "ok", ""); err != nil {
		t.Fatalf("done: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := jobwake.AcquireJobLock(testRoot, job); !errors.Is(err, jobwake.ErrAlreadyTerminal) {
		t.Fatalf("acquire after the terminal and release: err = %v, want jobwake.ErrAlreadyTerminal", err)
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

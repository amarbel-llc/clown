package jugglerrun

import (
	"context"
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

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

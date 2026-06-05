package jobwake

import (
	"encoding/json"
	"testing"
)

func TestRecordJSONRoundTrip(t *testing.T) {
	r := Record{V: 1, Job: "b-1", Session: "repo/branch", Source: "moxy",
		Type: TypeSucceeded, Seq: 2, TS: "2026-06-05T00:00:00Z", Message: "ok"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != r {
		t.Fatalf("round trip mismatch: %+v != %+v", back, r)
	}
}

func TestWakePolicy(t *testing.T) {
	for _, ty := range []string{TypeSucceeded, TypeFailed, TypeCancelled, TypeInterrupted} {
		if !IsTerminal(ty) || !IsWaking(ty) {
			t.Errorf("%s must be terminal and waking", ty)
		}
	}
	for _, ty := range []string{TypeStarted, TypeProgress} {
		if IsTerminal(ty) || IsWaking(ty) {
			t.Errorf("%s must not be terminal or waking", ty)
		}
	}
	if IsWaking("needs-attention") {
		t.Error("reserved non-terminal types must not wake in v1")
	}
}

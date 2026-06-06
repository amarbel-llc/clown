package jobwake

import (
	"encoding/json"
	"strings"
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
	// message is the non-terminal waking class (RFC-0009 §5).
	if IsTerminal(TypeMessage) {
		t.Error("message must not be terminal")
	}
	if !IsWaking(TypeMessage) {
		t.Error("message must wake")
	}
	if IsWaking("needs-attention") {
		t.Error("reserved types must not wake")
	}
}

func TestRecordFromOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Record{V: 1, Job: "j", Session: "k", Source: "s",
		Type: TypeSucceeded, TS: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"from"`) {
		t.Fatalf("empty from must be omitted, got %s", b)
	}

	r := Record{V: 1, Job: "j", Session: "k", Source: "s", From: "other/sender",
		Type: TypeMessage, TS: "t", Message: "hi"}
	b, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != r {
		t.Fatalf("from must round-trip: %+v != %+v", back, r)
	}
}

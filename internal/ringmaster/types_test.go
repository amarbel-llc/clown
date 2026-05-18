package ringmaster

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInstance_RoundTripJSON(t *testing.T) {
	in := Instance{
		Alias:     "qwen3-coder",
		Model:     "qwen3-coder",
		Port:      43219,
		PID:       91234,
		Bind:      "127.0.0.1",
		StartedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Instance
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

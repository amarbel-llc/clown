package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clown#334: the pin's source rides the same record as the pin, is returned
// with it, and is absent for records that never carried one.
func TestPinRecordOfReturnsTheSource(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	const id = "11111111-2222-3333-4444-555555555555"

	if err := RecordSession(NameRecord{SessionID: id, Profile: "p", ProfileDigest: "d1"}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := PinRecordOf(id)
	if err != nil || !ok || rec.Digest != "d1" || rec.Source != "" {
		t.Fatalf("first pin = %+v, %v, %v; want d1 with no source", rec, ok, err)
	}

	if err := RecordSession(NameRecord{SessionID: id, Profile: "p", ProfileDigest: "d2", ProfilePinSource: PinSourceUserConfirm}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err = PinRecordOf(id)
	if err != nil || !ok || rec.Digest != "d2" || rec.Source != PinSourceUserConfirm {
		t.Fatalf("re-pin = %+v, %v, %v; want d2 approved by user-confirm", rec, ok, err)
	}

	b, err := os.ReadFile(filepath.Join(state, "clown", "session-names.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "profile_pin_source") || !strings.Contains(lines[1], `"profile_pin_source":"user-confirm"`) {
		t.Errorf("sidecar:\n%s\nwant the key only on the approved record", b)
	}
}

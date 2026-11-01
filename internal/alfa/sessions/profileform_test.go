package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clown#298: a pin record can carry the redacted form of the pinned profile,
// looked up by digest.

func sidecarBytes(t *testing.T, state string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "clown", "session-names.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProfileFormRoundTripsByDigest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	form := json.RawMessage(`{"form":1,"name":"p1"}`)
	if err := RecordSession(NameRecord{SessionID: "id-1", Name: "bozo", Profile: "p1", ProfileDigest: "aaa", ProfileForm: form}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ProfileFormFor("aaa")
	if err != nil || !ok || string(got) != string(form) {
		t.Fatalf("ProfileFormFor(aaa) = %s, %v, %v; want %s", got, ok, err, form)
	}
	if _, ok, err := ProfileFormFor("bbb"); ok || err != nil {
		t.Errorf("ProfileFormFor(bbb) = ok %v, err %v; want not found", ok, err)
	}
	if pin, ok, err := ProfileOf("id-1"); !ok || err != nil || pin != (ProfilePin{Name: "p1", Digest: "aaa"}) {
		t.Errorf("ProfileOf(id-1) = %+v, %v, %v", pin, ok, err)
	}
}

// The last record carrying a form for the digest wins; a later record for
// the same digest without one does not hide it.
func TestProfileFormLastRecordWins(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, rec := range []NameRecord{
		{SessionID: "id-1", Profile: "p1", ProfileDigest: "aaa", ProfileForm: json.RawMessage(`{"v":1}`)},
		{SessionID: "id-2", Profile: "p1", ProfileDigest: "aaa", ProfileForm: json.RawMessage(`{"v":2}`)},
		{SessionID: "id-3", Profile: "p1", ProfileDigest: "aaa"},
	} {
		if err := RecordSession(rec); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok, _ := ProfileFormFor("aaa"); !ok || string(got) != `{"v":2}` {
		t.Errorf("ProfileFormFor = %s, %v; want the last stored form", got, ok)
	}
}

// Over the cap the form is dropped, the pin is kept, and the record says the
// form is absent. The record is still one line.
func TestProfileFormOverCapKeepsPinOnly(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	big := json.RawMessage(`{"x":"` + strings.Repeat("a", MaxProfileFormBytes) + `"}`)
	if err := RecordSession(NameRecord{SessionID: "id-1", Profile: "p1", ProfileDigest: "aaa", ProfileForm: big}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ProfileFormFor("aaa"); ok || err != nil {
		t.Errorf("ProfileFormFor over cap = ok %v, err %v; want no form", ok, err)
	}
	if pin, ok, err := ProfileOf("id-1"); !ok || err != nil || pin.Digest != "aaa" {
		t.Errorf("ProfileOf over cap = %+v, %v, %v; want the pin kept", pin, ok, err)
	}
	b := sidecarBytes(t, state)
	if strings.Count(b, "\n") != 1 || !strings.Contains(b, `"profile_form_omitted"`) || strings.Contains(b, "aaaaaaaa") {
		t.Errorf("sidecar = %.300s; want one line recording the omitted form", b)
	}
}

// A form is stored only alongside a profile, and invalid JSON is not stored.
func TestProfileFormNeedsAProfileAndValidJSON(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if err := RecordSession(NameRecord{SessionID: "id-1", Name: "bozo", ProfileForm: json.RawMessage(`{"v":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(NameRecord{SessionID: "id-2", Profile: "p1", ProfileDigest: "aaa", ProfileForm: json.RawMessage(`{not json`)}); err != nil {
		t.Fatal(err)
	}
	b := sidecarBytes(t, state)
	if strings.Contains(b, `"profile_form"`) || strings.Contains(b, "not json") {
		t.Errorf("sidecar = %s; want no stored form", b)
	}
	if pin, ok, err := ProfileOf("id-2"); !ok || err != nil || pin.Digest != "aaa" {
		t.Errorf("ProfileOf(id-2) = %+v, %v, %v; want the pin kept", pin, ok, err)
	}
}

// Lines written before forms existed keep loading, and a line a newer
// clown wrote with fields this build does not know still yields its pin.
func TestProfileFormCompatibility(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "clown")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := `{"session_id":"id-old","name":"bozo","profile":"p1","profile_digest":"aaa","ts":"2026-01-01T00:00:00Z"}` + "\n" +
		`{"session_id":"id-new","profile":"p2","profile_digest":"bbb","profile_form":{"form":1},"future_field":7,"ts":"2026-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session-names.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	if pin, ok, err := ProfileOf("id-old"); !ok || err != nil || pin.Digest != "aaa" {
		t.Errorf("ProfileOf(id-old) = %+v, %v, %v", pin, ok, err)
	}
	if _, ok, err := ProfileFormFor("aaa"); ok || err != nil {
		t.Errorf("ProfileFormFor(aaa) on a pre-form line = ok %v, err %v", ok, err)
	}
	if got, ok, err := ProfileFormFor("bbb"); !ok || err != nil || string(got) != `{"form":1}` {
		t.Errorf("ProfileFormFor(bbb) = %s, %v, %v", got, ok, err)
	}
}

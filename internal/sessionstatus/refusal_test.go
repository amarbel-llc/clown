package sessionstatus

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Without a refusal the key is absent, so a reader that predates the field
// and one that expects it both see an ordinary file.
func TestRestartRefusalIsOmittedUntilSet(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "restart_refusal") {
		t.Fatalf("a fresh status carries a refusal: %s", b)
	}
	if st := readStatus(t, path); st.RestartRefusal != nil || st.Version != 1 {
		t.Fatalf("status = %+v, want no refusal and version 1", st)
	}
}

// A refusal is written at once (not coalesced), stamped when the caller left
// the time out, bounded, and replaced by a later one; the rest of the status
// is untouched.
func TestSetRestartRefusal(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	w.TurnStarted(Payload{SessionID: "sess-1"})

	w.SetRestartRefusal(RestartRefusal{Kind: RefusalProfileGate, Profile: "worker", Text: "the relaunch's profile gate would refuse\nsecond line\x1b[31m" + strings.Repeat("x", 2*MaxRefusalText)})
	st := readStatus(t, path)
	r := st.RestartRefusal
	if r == nil || r.Kind != "profile-gate" || r.Profile != "worker" {
		t.Fatalf("refusal = %+v", r)
	}
	if _, err := time.Parse(time.RFC3339Nano, r.At); err != nil {
		t.Fatalf("at %q: %v", r.At, err)
	}
	if strings.ContainsAny(r.Text, "\n\x1b") || len(r.Text) > MaxRefusalText || !strings.HasPrefix(r.Text, "the relaunch's profile gate would refuse") {
		t.Fatalf("text %q: want one printable bounded line", r.Text)
	}
	if !st.TurnRunning || st.ClaudeSessionID != "sess-1" {
		t.Fatalf("the refusal disturbed the rest of the status: %+v", st)
	}

	time.Sleep(2 * time.Millisecond)
	w.SetRestartRefusal(RestartRefusal{Kind: RefusalTurnStarted, Text: "a turn started during the countdown"})
	r2 := readStatus(t, path).RestartRefusal
	if r2 == nil || r2.Kind != "turn-started" || r2.Profile != "" || r2.At <= r.At {
		t.Fatalf("second refusal = %+v after %+v, want it to replace the first with a later at", r2, r)
	}
}

// A successful launch opens a new Writer, whose file has no refusal: the
// field is cleared by the next launch without anyone clearing it.
func TestNewWriterStartsWithoutTheOldRefusal(t *testing.T) {
	w, path, _ := newTestWriter(t)
	w.Start()
	w.SetRestartRefusal(RestartRefusal{Kind: RefusalResume, Text: "refused"})
	w2, err := Open("p-1", "repo/branch", nil)
	if err != nil {
		t.Fatal(err)
	}
	w2.Start()
	if st := readStatus(t, path); st.RestartRefusal != nil {
		t.Fatalf("refusal survived the next launch: %+v", st.RestartRefusal)
	}
}

func TestNilWriterRefusalIsANoOp(t *testing.T) {
	var w *Writer
	w.SetRestartRefusal(RestartRefusal{Kind: RefusalResume})
}

// The relaunch's own refusal has no Writer yet: WriteRestartRefusal writes the
// file once, for a frontend about to exit.
func TestWriteRestartRefusalWritesTheWholeFile(t *testing.T) {
	_, path, _ := newTestWriter(t)
	if err := WriteRestartRefusal("p-1", "repo/branch", RestartRefusal{Kind: RefusalResume, Profile: "worker", PinnedDigest: "aaa", CurrentDigest: "bbb", Text: "profile worker changed"}); err != nil {
		t.Fatal(err)
	}
	st := readStatus(t, path)
	r := st.RestartRefusal
	if st.Principal != "p-1" || st.SpinclassSessionKey != "repo/branch" || st.FrontendPID != os.Getpid() || r == nil || r.Kind != "resume" || r.PinnedDigest != "aaa" || r.CurrentDigest != "bbb" {
		t.Fatalf("status = %+v refusal %+v", st, r)
	}
	var raw map[string]json.RawMessage
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &raw); err != nil || raw["restart_refusal"] == nil {
		t.Fatalf("file lacks restart_refusal: %s", b)
	}
}

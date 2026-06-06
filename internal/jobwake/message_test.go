package jobwake

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestMessageWritesSingleRecordJob covers the RFC-0009 §4 standalone
// waking-event-job carve-out: one producer call yields one self-contained
// single-record job (no started, no terminal) in the TARGET channel, with
// type message, seq 0, the sender key in `from`, and a flattened body.
func TestMessageWritesSingleRecordJob(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "self")

	id, err := Message("other", "spinclass", "self", "line1\nline2", "ref-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "msg-") {
		t.Fatalf("job id must be msg-<8hex>, got %q", id)
	}

	recs, err := ReadJob(ChannelID("other"), id)
	if err != nil {
		t.Fatalf("reading target channel: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want exactly one record (standalone waking-event job), got %+v", recs)
	}
	r := recs[0]
	if r.Type != TypeMessage || r.Seq != 0 || r.V != 1 {
		t.Fatalf("want type message seq 0 v 1, got %+v", r)
	}
	if r.Session != "other" || r.From != "self" || r.Source != "spinclass" || r.ResultRef != "ref-1" {
		t.Fatalf("bad record fields: %+v", r)
	}
	if strings.ContainsAny(r.Message, "\n\r") {
		t.Fatalf("message body must be newline-flattened, got %q", r.Message)
	}

	// The sender's own channel must not have the job.
	if _, err := ReadJob(ChannelID("self"), id); !os.IsNotExist(err) {
		t.Fatalf("sender channel must not have the message job; err = %v", err)
	}
}

// TestMessageEmptyTargetResolvesSession mirrors resolveSession: an empty
// target writes to the current session's channel.
func TestMessageEmptyTargetResolvesSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "self")

	id, err := Message("", "s", "", "hi", "")
	if err != nil {
		t.Fatal(err)
	}
	recs, err := ReadJob(ChannelID("self"), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Session != "self" || recs[0].From != "" {
		t.Fatalf("want one record in own channel with empty from, got %+v", recs)
	}
}

// TestMessageDirectedSendsNudge proves a directed message nudges the target
// channel's socket like any other waking emit.
func TestMessageDirectedSendsNudge(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "self")

	cid := ChannelID("other")
	conn, err := bindNudge(cid)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	id, err := Message("other", "s", "self", "ping", "")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 512)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(buf[:n])), "1|"+id+"|message"; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// TestMessageBroadcastLandsInBroadcastChannelNoNudge: a `--target '*'`
// message lands in ChannelID(BroadcastKey) and sends NO nudge (RFC-0009 §6);
// the monitors' rescan tick is the delivery path. The negative-receive is
// asserted with a bound broadcast socket and a short read deadline.
func TestMessageBroadcastLandsInBroadcastChannelNoNudge(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "self")

	bcid := ChannelID(BroadcastKey)
	conn, err := bindNudge(bcid)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	id, err := Message(BroadcastKey, "s", "self", "to everyone", "")
	if err != nil {
		t.Fatal(err)
	}

	recs, err := ReadJob(bcid, id)
	if err != nil {
		t.Fatalf("reading broadcast channel: %v", err)
	}
	if len(recs) != 1 || recs[0].Session != BroadcastKey || recs[0].Type != TypeMessage {
		t.Fatalf("want one message record with session %q, got %+v", BroadcastKey, recs)
	}

	conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, 512)
	if n, _, err := conn.ReadFromUnix(buf); err == nil {
		t.Fatalf("broadcast message must not nudge, received %q", string(buf[:n]))
	}
}

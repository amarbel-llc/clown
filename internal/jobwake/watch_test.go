package jobwake

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// drainWatch runs Watch in a goroutine, collects emitted records, and cancels
// once the first emit arrives or after a short idle window with no emit. It
// returns whatever was collected.
func drainWatch(t *testing.T, sessionKey string) []Record {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var got []Record
	emitted := make(chan struct{}, 64)

	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, sessionKey, func(r Record) error {
			mu.Lock()
			got = append(got, r)
			mu.Unlock()
			select {
			case emitted <- struct{}{}:
			default:
			}
			return nil
		})
	}()

	// Wait for the first emit, or give up after an idle window. Replay happens
	// synchronously before Watch blocks, so a ready event is emitted promptly.
	select {
	case <-emitted:
	case <-time.After(750 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]Record, len(got))
	copy(out, got)
	return out
}

func TestWatchReplaysUnackedTerminalOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	_ = Progress("", id, "p")
	_ = Done("", id, TypeSucceeded, "ok", "")

	emitted := drainWatch(t, "k")
	if len(emitted) != 1 || emitted[0].Type != TypeSucceeded {
		t.Fatalf("want one succeeded emit, got %+v", emitted)
	}

	emitted2 := drainWatch(t, "k")
	if len(emitted2) != 0 {
		t.Fatalf("second watch must replay nothing, got %+v", emitted2)
	}
}

func TestWatchNeverEmitsProgress(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, _ := Start(StartOpts{Source: "s"})
	_ = Progress("", id, "halfway")
	_ = Done("", id, TypeFailed, "boom", "ref")

	emitted := drainWatch(t, "k")
	if len(emitted) != 1 {
		t.Fatalf("want exactly one (terminal) emit, got %+v", emitted)
	}
	if emitted[0].Type != TypeFailed {
		t.Fatalf("only the terminal record may wake, got %+v", emitted[0])
	}
}

// TestReplayOnceEmitsDirectedMessageOnce: a directed `message` is a waking
// event — ReplayOnce emits it once and the ack gates re-emission thereafter.
func TestReplayOnceEmitsDirectedMessageOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "sender")

	id, err := Message("k", "spinclass", "sender", "ping", "")
	if err != nil {
		t.Fatal(err)
	}

	var first []Record
	if err := ReplayOnce("k", func(r Record) error { first = append(first, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Type != TypeMessage || first[0].Job != id || first[0].From != "sender" {
		t.Fatalf("want one message emit with from, got %+v", first)
	}

	var second []Record
	if err := ReplayOnce("k", func(r Record) error { second = append(second, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second ReplayOnce must emit nothing (acked), got %+v", second)
	}
}

// TestWatchEmitsDirectedMessage: the long-running monitor also wakes on a
// directed message (the non-terminal waking class).
func TestWatchEmitsDirectedMessage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "sender")

	if _, err := Message("k", "s", "sender", "ping", ""); err != nil {
		t.Fatal(err)
	}
	emitted := drainWatch(t, "k")
	if len(emitted) != 1 || emitted[0].Type != TypeMessage {
		t.Fatalf("want one message emit, got %+v", emitted)
	}
}

// replayBroadcast runs ReplayOnce for a reader session and returns what was
// emitted, failing the test on error.
func replayBroadcast(t *testing.T, reader string) []Record {
	t.Helper()
	var got []Record
	if err := ReplayOnce(reader, func(r Record) error { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestBroadcastCondvarSemantics pins the RFC-0009 §9 condvar contract:
//  1. a broadcast pre-existing a reader's FIRST attach is NOT emitted
//     (first attach initializes the per-reader ack at current end);
//  2. the persisted watermark means a broadcast sent AFTER first attach,
//     while the monitor is down, IS emitted on the next attach;
//  3. it is emitted exactly once (acked thereafter).
func TestBroadcastCondvarSemantics(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "sender")

	if _, err := Message(BroadcastKey, "s", "sender", "pre-attach", ""); err != nil {
		t.Fatal(err)
	}

	// First attach: init at end, nothing emitted.
	if got := replayBroadcast(t, "reader"); len(got) != 0 {
		t.Fatalf("first attach must not replay pre-existing broadcasts, got %+v", got)
	}

	// Broadcast while the monitor is down, post-attach.
	id, err := Message(BroadcastKey, "s", "sender", "post-attach", "")
	if err != nil {
		t.Fatal(err)
	}

	got := replayBroadcast(t, "reader")
	if len(got) != 1 || got[0].Job != id || got[0].Message != "post-attach" {
		t.Fatalf("post-attach broadcast must be emitted on next attach, got %+v", got)
	}

	if got := replayBroadcast(t, "reader"); len(got) != 0 {
		t.Fatalf("broadcast must be emitted exactly once per reader, got %+v", got)
	}
}

// TestBroadcastTwoReadersIndependentAcks: two distinct reader sessions each
// receive a post-attach broadcast exactly once, via independent per-reader
// ack files in the broadcast channel dir.
func TestBroadcastTwoReadersIndependentAcks(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "sender")

	// Both readers attach (init-at-end) before the broadcast.
	if got := replayBroadcast(t, "reader-1"); len(got) != 0 {
		t.Fatalf("reader-1 first attach must emit nothing, got %+v", got)
	}
	if got := replayBroadcast(t, "reader-2"); len(got) != 0 {
		t.Fatalf("reader-2 first attach must emit nothing, got %+v", got)
	}

	id, err := Message(BroadcastKey, "s", "sender", "to everyone", "")
	if err != nil {
		t.Fatal(err)
	}

	for _, reader := range []string{"reader-1", "reader-2"} {
		got := replayBroadcast(t, reader)
		if len(got) != 1 || got[0].Job != id {
			t.Fatalf("%s must receive the broadcast once, got %+v", reader, got)
		}
		if again := replayBroadcast(t, reader); len(again) != 0 {
			t.Fatalf("%s must not receive the broadcast twice, got %+v", reader, again)
		}
	}

	// Independent ack files exist for both readers in the broadcast dir.
	bcid := ChannelID(BroadcastKey)
	for _, reader := range []string{"reader-1", "reader-2"} {
		if _, err := os.Stat(AckFileFor(bcid, ChannelID(reader))); err != nil {
			t.Fatalf("missing per-reader ack for %s: %v", reader, err)
		}
	}
}

// TestBroadcastSuppressesSenderSelfEcho pins the #114/#115 fix: a session's
// broadcast must not wake the sender's own monitor. The sender and a bystander
// both attach (init-at-end), the sender broadcasts post-attach, and the
// sender's own replay emits nothing — yet the record is ACKED (the load-bearing
// half: a suppressed record must not linger unacked in the sender's broadcast
// ack map). The bystander still receives it exactly once.
func TestBroadcastSuppressesSenderSelfEcho(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "sender")

	if got := replayBroadcast(t, "sender"); len(got) != 0 {
		t.Fatalf("sender first attach must emit nothing, got %+v", got)
	}
	if got := replayBroadcast(t, "bystander"); len(got) != 0 {
		t.Fatalf("bystander first attach must emit nothing, got %+v", got)
	}

	id, err := Message(BroadcastKey, "s", "sender", "hello all", "")
	if err != nil {
		t.Fatal(err)
	}

	// The sender's own monitor must NOT be woken by its own broadcast.
	if got := replayBroadcast(t, "sender"); len(got) != 0 {
		t.Fatalf("sender must not self-echo its own broadcast, got %+v", got)
	}
	// The suppressed record must be acked, so it does not re-surface and does
	// not sit permanently unacked (#114).
	bcid := ChannelID(BroadcastKey)
	a := loadAckPath(AckFileFor(bcid, ChannelID("sender")))
	if _, ok := a.Acked[id]; !ok {
		t.Fatalf("sender's suppressed broadcast must be acked, ack map: %+v", a.Acked)
	}

	// A different reader still receives it exactly once.
	got := replayBroadcast(t, "bystander")
	if len(got) != 1 || got[0].Job != id {
		t.Fatalf("bystander must receive the broadcast once, got %+v", got)
	}
}

// TestDirectedSelfMessageStillWakes locks the #114 carve-out: self-echo
// suppression applies to the broadcast channel only, never the own-channel
// path, so a deliberate directed self-message (--target <own-key> with
// from=<own-key>, the "remind myself" case) still wakes.
func TestDirectedSelfMessageStillWakes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "k")

	if _, err := Message("k", "s", "k", "note to self", ""); err != nil {
		t.Fatal(err)
	}
	got := replayBroadcast(t, "k") // ReplayOnce services the own channel too
	if len(got) != 1 || got[0].Type != TypeMessage {
		t.Fatalf("directed self-message must still wake, got %+v", got)
	}
}

func TestReplayOnceEmitsUnackedThenNothing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", shortRuntimeDir(t))
	t.Setenv("CLOWN_SESSION_ID", "k")
	id, err := Start(StartOpts{Source: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Done("", id, TypeSucceeded, "ok", ""); err != nil {
		t.Fatal(err)
	}

	var first []Record
	if err := ReplayOnce("k", func(r Record) error { first = append(first, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Type != TypeSucceeded {
		t.Fatalf("first ReplayOnce: want one succeeded emit, got %+v", first)
	}

	var second []Record
	if err := ReplayOnce("k", func(r Record) error { second = append(second, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second ReplayOnce must emit nothing (acked), got %+v", second)
	}
}

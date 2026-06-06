package jobwake

import (
	"context"
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

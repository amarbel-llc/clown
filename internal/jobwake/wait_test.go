package jobwake

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitEnv isolates the journal under a temp XDG_STATE_HOME and pins the session
// key. WaitDone touches only the journal (no nudge socket), so no runtime dir is
// needed.
func waitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
}

// fastWait drives the internal poll core with a short interval so the
// block-until-terminal tests do not wait a full rescan second.
func fastWait(ctx context.Context, t *testing.T, target, jobID string) (Record, error) {
	t.Helper()
	return waitDoneChannel(ctx, ChannelID(resolveSession(target)), jobID, 5*time.Millisecond)
}

func TestWaitDoneReturnsImmediatelyWhenAlreadyTerminal(t *testing.T) {
	waitEnv(t)
	id, err := Start(StartOpts{Source: "moxy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Done("", id, TypeSucceeded, "all good", "madder://blobs/x"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rec, err := fastWait(ctx, t, "", id)
	if err != nil {
		t.Fatalf("WaitDone: %v", err)
	}
	if rec.Type != TypeSucceeded || rec.Message != "all good" || rec.ResultRef != "madder://blobs/x" {
		t.Fatalf("terminal record = %+v, want succeeded with message + result_ref", rec)
	}
}

func TestWaitDoneBlocksUntilTerminal(t *testing.T) {
	waitEnv(t)
	id, err := Start(StartOpts{Source: "moxy"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = Done("", id, TypeFailed, "boom", "")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rec, err := fastWait(ctx, t, "", id)
	if err != nil {
		t.Fatalf("WaitDone: %v", err)
	}
	if rec.Type != TypeFailed {
		t.Fatalf("terminal record type = %q, want failed", rec.Type)
	}
}

func TestWaitDoneUnknownJobErrors(t *testing.T) {
	waitEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := fastWait(ctx, t, "", "job-does-not-exist"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("WaitDone err = %v, want ErrJobNotFound", err)
	}
}

func TestWaitDoneContextDeadlineWhileRunning(t *testing.T) {
	waitEnv(t)
	id, err := Start(StartOpts{Source: "moxy"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := fastWait(ctx, t, "", id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDone err = %v, want context.DeadlineExceeded", err)
	}
}

func TestWaitDoneInvalidJobID(t *testing.T) {
	waitEnv(t)
	if _, err := fastWait(context.Background(), t, "", "../escape"); !errors.Is(err, ErrInvalidJobID) {
		t.Fatalf("WaitDone err = %v, want ErrInvalidJobID", err)
	}
}

// TestWaitDonePublicAPIUsesDefaultInterval exercises the exported WaitDone (not
// the injectable core) so the public signature stays covered.
func TestWaitDonePublicAPIUsesDefaultInterval(t *testing.T) {
	waitEnv(t)
	id, err := Start(StartOpts{Source: "moxy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Done("", id, TypeCancelled, "", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rec, err := WaitDone(ctx, "", id)
	if err != nil {
		t.Fatalf("WaitDone: %v", err)
	}
	if rec.Type != TypeCancelled {
		t.Fatalf("terminal record type = %q, want cancelled", rec.Type)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"
)

// nullLogger discards everything; tests use this unless they need to
// inspect log output.
type nullLogger struct{}

func (nullLogger) Printf(format string, args ...any) {}

// recordingLogger captures Printf calls for inspection.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingLogger) Printf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, format)
	_ = args
}

// pipePair gives the test direct, in-memory access to the bytes the
// bridge would have written to stdin and read from stdout. The caller
// drives the wrapped child's "behavior" by writing into stdoutWriter
// (which the translator reads as if from the wrapped child).
func pipePair() (stdin *bytes.Buffer, stdoutReader io.Reader, stdoutWriter io.Writer) {
	r, w := io.Pipe()
	return &bytes.Buffer{}, r, w
}

func TestTranslator_RequestResponseRoundtrip(t *testing.T) {
	stdin, stdoutR, stdoutW := pipePair()
	tr := newTranslator(stdin, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = tr.Run(ctx) }()

	// In a separate goroutine, simulate the wrapped child responding
	// to whatever shows up on stdin by reading the request and
	// emitting a matching response on stdoutW.
	go func() {
		// Wait briefly for the request to land in stdin.
		deadline := time.Now().Add(time.Second)
		var msg map[string]any
		for time.Now().Before(deadline) {
			if stdin.Len() == 0 {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			break
		}
		_ = json.Unmarshal(stdin.Bytes(), &msg)
		// Echo as a response with the same id.
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      msg["id"],
			"result":  map[string]any{"echoed": msg["params"]},
		}
		raw, _ := json.Marshal(resp)
		_, _ = stdoutW.Write(append(raw, '\n'))
	}()

	body := []byte(`{"jsonrpc":"2.0","id":42,"method":"ping","params":{"x":1}}`)
	resp, err := tr.SendRequest(ctx, "42", body)
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, resp)
	}
	if id, ok := got["id"].(float64); !ok || id != 42 {
		t.Errorf("response id = %v (%T), want 42 (float64)", got["id"], got["id"])
	}
}

func TestTranslator_NotificationFireAndForget(t *testing.T) {
	stdin, stdoutR, _ := pipePair()
	tr := newTranslator(stdin, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()

	body := []byte(`{"jsonrpc":"2.0","method":"notifications/log","params":{"level":"info"}}`)
	if err := tr.SendNotification(body); err != nil {
		t.Fatalf("SendNotification: %v", err)
	}
	// Verify it lands in the stdin buffer.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if stdin.Len() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !bytes.Contains(stdin.Bytes(), body) {
		t.Errorf("stdin did not receive notification body; got %q", stdin.Bytes())
	}
}

func TestTranslator_BroadcastUnmatchedToSubscribers(t *testing.T) {
	stdin, stdoutR, stdoutW := pipePair()
	tr := newTranslator(stdin, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()

	subA, cancelA := tr.Subscribe()
	defer cancelA()
	subB, cancelB := tr.Subscribe()
	defer cancelB()

	// Server-initiated notification — has method, no id.
	notif := []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	_, _ = stdoutW.Write(append(notif, '\n'))

	for _, sub := range []<-chan json.RawMessage{subA, subB} {
		select {
		case got := <-sub:
			if !bytes.Contains(got, []byte(`tools/list_changed`)) {
				t.Errorf("subscriber received unexpected message: %s", got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber did not receive broadcast within 1 s")
		}
	}
}

func TestTranslator_ResponseToUnknownIdIsLogged(t *testing.T) {
	stdin, stdoutR, stdoutW := pipePair()
	rl := &recordingLogger{}
	tr := newTranslator(stdin, stdoutR, rl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()

	// Response with id that has no pending request.
	orphan := []byte(`{"jsonrpc":"2.0","id":999,"result":"orphan"}`)
	_, _ = stdoutW.Write(append(orphan, '\n'))

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		rl.mu.Lock()
		n := len(rl.lines)
		rl.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if len(rl.lines) == 0 {
		t.Errorf("expected at least one log line for orphan response")
	}
}

func TestTranslator_RequestContextCancelDropsPending(t *testing.T) {
	stdin, stdoutR, _ := pipePair()
	tr := newTranslator(stdin, stdoutR, nullLogger{})
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go func() { _ = tr.Run(runCtx) }()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		reqCancel()
	}()

	_, err := tr.SendRequest(reqCtx, "7",
		[]byte(`{"jsonrpc":"2.0","id":7,"method":"slow"}`))
	if err == nil {
		t.Fatal("expected ctx.Err, got nil")
	}
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	// pending should be cleaned up.
	tr.pendingMu.Lock()
	defer tr.pendingMu.Unlock()
	if _, ok := tr.pending["7"]; ok {
		t.Errorf("pending map still contains id 7 after ctx cancel")
	}
}

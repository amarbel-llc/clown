package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
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

// safeBuffer is a bytes.Buffer guarded by a mutex so the translator's
// runWriter goroutine can write to it while the test goroutine inspects
// it via Bytes/Len. Plain bytes.Buffer is not safe for concurrent use
// and trips the race detector.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

// Bytes returns a copy of the buffered bytes. Returning a copy keeps
// callers safe from later writes mutating the underlying slice.
func (s *safeBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

// pipePair gives the test direct, in-memory access to the bytes the
// bridge would have written to stdin and read from stdout. The caller
// drives the wrapped child's "behavior" by writing into stdoutWriter
// (which the translator reads as if from the wrapped child).
func pipePair() (stdin *safeBuffer, stdoutReader io.Reader, stdoutWriter io.Writer) {
	r, w := io.Pipe()
	return &safeBuffer{}, r, w
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

// TestTranslator_ConcurrentMixedIDRequests fires N concurrent SendRequest
// calls with distinct ids against a mock child that responds with
// randomized per-request latency. This forces responses to arrive
// out-of-order on stdout. The test asserts that each caller receives the
// response with the matching id and the matching echoed payload — i.e.
// the translator's id-correlation invariant holds under concurrency.
//
// Regression guard for #31.
func TestTranslator_ConcurrentMixedIDRequests(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	t.Cleanup(func() { stdinW.Close(); stdoutW.Close() })

	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()

	// Mock child: read framed request lines off stdin, echo each one back
	// after a per-request delay. Each response is written from its own
	// goroutine so they race with each other and arrive out-of-order.
	// io.PipeWriter.Write is goroutine-safe and gates parallel writes
	// sequentially, so each response line lands atomically.
	rng := rand.New(rand.NewPCG(1, 2))
	var rngMu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stdinR)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			var probe struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Token string `json:"token"`
				} `json:"params"`
			}
			if err := json.Unmarshal(line, &probe); err != nil {
				continue
			}
			if len(probe.ID) == 0 || string(probe.ID) == "null" {
				continue // notification — no response expected
			}
			rngMu.Lock()
			delay := time.Duration(rng.IntN(15)+1) * time.Millisecond
			rngMu.Unlock()
			id := string(probe.ID)
			token := probe.Params.Token
			go func() {
				time.Sleep(delay)
				resp := fmt.Sprintf(
					`{"jsonrpc":"2.0","id":%s,"result":{"echoedToken":%q}}`+"\n",
					id, token)
				_, _ = stdoutW.Write([]byte(resp))
			}()
		}
	}()

	const N = 32
	type result struct {
		idKey string
		resp  json.RawMessage
		err   error
	}
	results := make(chan result, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			idKey := strconv.Itoa(i)
			token := fmt.Sprintf("tok-%d", i)
			body := []byte(fmt.Sprintf(
				`{"jsonrpc":"2.0","id":%d,"method":"echo","params":{"token":%q}}`,
				i, token))
			reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
			defer reqCancel()
			resp, err := tr.SendRequest(reqCtx, idKey, body)
			results <- result{idKey: idKey, resp: resp, err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	seen := map[string]bool{}
	for r := range results {
		if r.err != nil {
			t.Errorf("id %s: SendRequest error: %v", r.idKey, r.err)
			continue
		}
		if seen[r.idKey] {
			t.Errorf("id %s: result returned twice", r.idKey)
		}
		seen[r.idKey] = true

		var got struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				EchoedToken string `json:"echoedToken"`
			} `json:"result"`
		}
		if err := json.Unmarshal(r.resp, &got); err != nil {
			t.Errorf("id %s: response not valid JSON: %v\n%s", r.idKey, err, r.resp)
			continue
		}
		if string(got.ID) != r.idKey {
			t.Errorf("id %s: response id = %s, cross-contamination", r.idKey, got.ID)
		}
		wantToken := fmt.Sprintf("tok-%s", r.idKey)
		if got.Result.EchoedToken != wantToken {
			t.Errorf("id %s: echoed token = %q, want %q",
				r.idKey, got.Result.EchoedToken, wantToken)
		}
	}
	if len(seen) != N {
		t.Errorf("got %d unique results, want %d", len(seen), N)
	}

	// Pending map should be drained — every request either completed or
	// timed out (we'd have caught the latter via err != nil above).
	tr.pendingMu.Lock()
	defer tr.pendingMu.Unlock()
	if n := len(tr.pending); n != 0 {
		t.Errorf("pending map has %d leftover entries after all requests resolved", n)
	}
}

// TestTranslator_BroadcastInterleavedWithRequests verifies that
// server-initiated notifications routed through the broadcast path do
// not interfere with id-correlated request/response routing when both
// happen concurrently.
//
// Regression guard for the optional half of #31.
func TestTranslator_BroadcastInterleavedWithRequests(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	t.Cleanup(func() { stdinW.Close(); stdoutW.Close() })

	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()

	sub, cancelSub := tr.Subscribe()
	defer cancelSub()

	// Mock child: respond to each request with a slight delay; also
	// inject server-initiated notifications between responses.
	go func() {
		sc := bufio.NewScanner(stdinR)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		notifCounter := 0
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			var probe struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(line, &probe)
			if len(probe.ID) == 0 {
				continue
			}
			id := string(probe.ID)
			notifCounter++
			notifIdx := notifCounter
			go func() {
				time.Sleep(time.Duration(notifIdx%5+1) * time.Millisecond)
				notif := fmt.Sprintf(
					`{"jsonrpc":"2.0","method":"notifications/progress","params":{"seq":%d}}`+"\n",
					notifIdx)
				_, _ = stdoutW.Write([]byte(notif))
				resp := fmt.Sprintf(
					`{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`+"\n", id)
				_, _ = stdoutW.Write([]byte(resp))
			}()
		}
	}()

	const N = 16
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			idKey := strconv.Itoa(i)
			body := []byte(fmt.Sprintf(
				`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, i))
			reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
			defer reqCancel()
			resp, err := tr.SendRequest(reqCtx, idKey, body)
			if err != nil {
				errs <- fmt.Errorf("id %s: %w", idKey, err)
				return
			}
			var got struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(resp, &got); err != nil {
				errs <- fmt.Errorf("id %s: invalid resp: %w", idKey, err)
				return
			}
			if string(got.ID) != idKey {
				errs <- fmt.Errorf("id %s: cross-contamination got id=%s",
					idKey, got.ID)
			}
		}(i)
	}

	// Drain at least some broadcast messages while requests are in flight.
	gotNotifs := 0
	doneDraining := make(chan struct{})
	go func() {
		defer close(doneDraining)
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-deadline.C:
				return
			case msg := <-sub:
				if !bytes.Contains(msg, []byte(`notifications/progress`)) {
					t.Errorf("subscriber received non-notification: %s", msg)
				}
				gotNotifs++
				if gotNotifs >= N {
					return
				}
			}
		}
	}()

	wg.Wait()
	close(errs)
	<-doneDraining

	for err := range errs {
		t.Error(err)
	}
	if gotNotifs == 0 {
		t.Error("subscriber received zero broadcast notifications")
	}
}

// TestTranslator_IDReuseRejectedWhilePriorAbandoned regression-tests #50.
// After a SendRequest is canceled, the bridge holds the id in the
// "abandoned" state until the wrapped child's late response (if any)
// arrives. A second SendRequest reusing that id MUST be rejected with a
// duplicate-id-style error rather than receiving the prior call's late
// response (cross-contamination). Once the late response arrives and is
// dropped, the id becomes reusable again.
func TestTranslator_IDReuseRejectedWhilePriorAbandoned(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	t.Cleanup(func() { stdinW.Close(); stdoutW.Close() })

	rl := &recordingLogger{}
	tr := newTranslator(stdinW, stdoutR, rl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()

	stdinLines := make(chan []byte, 8)
	go func() {
		sc := bufio.NewScanner(stdinR)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			stdinLines <- append([]byte(nil), sc.Bytes()...)
		}
	}()

	// First call: ctx canceled before any response is emitted.
	req1Ctx, req1Cancel := context.WithCancel(ctx)
	req1Err := make(chan error, 1)
	go func() {
		_, err := tr.SendRequest(req1Ctx, "2",
			[]byte(`{"jsonrpc":"2.0","id":2,"method":"slow","params":{"which":"first"}}`))
		req1Err <- err
	}()

	select {
	case <-stdinLines:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach stdin")
	}
	req1Cancel()
	if err := <-req1Err; err != context.Canceled {
		t.Fatalf("first SendRequest err = %v, want context.Canceled", err)
	}

	// Second call reuses id "2" while the prior call is still abandoned.
	// Must reject — not register a new pending entry, not send to wire.
	_, err := tr.SendRequest(ctx, "2",
		[]byte(`{"jsonrpc":"2.0","id":2,"method":"normal","params":{"which":"second"}}`))
	if err == nil {
		t.Fatal("second SendRequest must reject id reuse against abandoned entry")
	}
	if !strings.Contains(err.Error(), "abandoned") {
		t.Errorf("error = %q, want substring \"abandoned\"", err)
	}
	select {
	case line := <-stdinLines:
		t.Errorf("rejected request must not reach the wire; got %s", line)
	case <-time.After(50 * time.Millisecond):
	}

	// Late response to the original call arrives — bridge logs+drops it
	// and clears the abandoned slot.
	late := []byte(`{"jsonrpc":"2.0","id":2,"result":{"which":"first-late"}}` + "\n")
	if _, err := stdoutW.Write(late); err != nil {
		t.Fatalf("write late response: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rl.mu.Lock()
		var sawAbandoned bool
		for _, line := range rl.lines {
			if strings.Contains(line, "response for abandoned id %s") {
				sawAbandoned = true
				break
			}
		}
		rl.mu.Unlock()
		if sawAbandoned {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// After the late response is consumed, id "2" becomes reusable.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tr.pendingMu.Lock()
		_, stillThere := tr.pending["2"]
		tr.pendingMu.Unlock()
		if !stillThere {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tr.pendingMu.Lock()
	_, stillThere := tr.pending["2"]
	tr.pendingMu.Unlock()
	if stillThere {
		t.Errorf("pending[\"2\"] should be cleared after late response was dropped")
	}
}

// TestTranslator_LateResponseAfterCancelLogsElapsed locks in the
// observability addition: when a SendRequest is canceled and the wrapped
// child eventually responds, the bridge logs an "abandoned id" line with
// an elapsed= measurement so operators can distinguish a response that
// arrived just-after-cancel from one that arrived much later.
func TestTranslator_LateResponseAfterCancelLogsElapsed(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	t.Cleanup(func() { stdinW.Close(); stdoutW.Close() })

	rl := &recordingLogger{}
	tr := newTranslator(stdinW, stdoutR, rl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()

	// Drain stdin so SendRequest unblocks the writer goroutine.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := stdinR.Read(buf); err != nil {
				return
			}
		}
	}()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		reqCancel()
	}()
	_, err := tr.SendRequest(reqCtx, "9",
		[]byte(`{"jsonrpc":"2.0","id":9,"method":"slow"}`))
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Wrapped child eventually responds.
	late := []byte(`{"jsonrpc":"2.0","id":9,"result":{"finally":true}}` + "\n")
	_, _ = stdoutW.Write(late)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rl.mu.Lock()
		var foundElapsed bool
		for _, line := range rl.lines {
			if strings.Contains(line, "response for abandoned id %s elapsed=%dms") {
				foundElapsed = true
				break
			}
		}
		rl.mu.Unlock()
		if foundElapsed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("did not see expected abandoned-id-with-elapsed log; got: %v", rl.lines)
}

// TestTranslator_RequestContextCancelMarksAbandoned verifies that ctx
// cancel transitions the pending entry to abandoned (respCh=nil) rather
// than deleting it. The abandoned entry is what blocks id reuse from
// cross-contaminating (#50) and is cleared by demux when the late
// response arrives.
func TestTranslator_RequestContextCancelMarksAbandoned(t *testing.T) {
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
	tr.pendingMu.Lock()
	defer tr.pendingMu.Unlock()
	entry, ok := tr.pending["7"]
	if !ok {
		t.Fatal("pending[\"7\"] should be retained as abandoned, not deleted")
	}
	if entry.respCh != nil {
		t.Errorf("pending[\"7\"].respCh = %v, want nil (abandoned)", entry.respCh)
	}
	if entry.seenAt.IsZero() {
		t.Errorf("pending[\"7\"].seenAt is zero, want a real time")
	}
}

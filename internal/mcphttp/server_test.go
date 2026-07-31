package mcphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// nullLogger discards every log line; the spine tests never inspect output.
type nullLogger struct{}

func (nullLogger) Printf(format string, args ...any) {}

// recordingLogger captures formatted log lines for assertions.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingLogger) Printf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recordingLogger) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// stubHandler is a minimal RequestHandler: SendRequest returns a canned
// response body regardless of input, notifications/subscriptions are no-ops.
type stubHandler struct {
	resp json.RawMessage
}

func (s stubHandler) SendRequest(ctx context.Context, idKey string, body []byte) (json.RawMessage, error) {
	return s.resp, nil
}

func (s stubHandler) SendNotification(body []byte) error { return nil }

func (s stubHandler) Subscribe() (<-chan json.RawMessage, func()) {
	ch := make(chan json.RawMessage)
	return ch, func() {}
}

// newTestServer wires a Server over the stub handler with heartbeats disabled
// (Streaming false → synchronous JSON path) so the canned response is asserted
// directly.
func newTestServer(t *testing.T, resp string) *httptest.Server {
	t.Helper()
	srv := NewServer(Config{
		Handler:   stubHandler{resp: json.RawMessage(resp)},
		Logger:    nullLogger{},
		Heartbeat: Heartbeat{}, // synchronous JSON path
	})
	return httptest.NewServer(http.HandlerFunc(srv.HandleMCP))
}

func TestServer_PostToolsListReturnsCannedResponse(t *testing.T) {
	canned := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a"}]}}`
	ts := newTestServer(t, canned)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, got)
	}
	if len(parsed.Result.Tools) != 1 || parsed.Result.Tools[0].Name != "a" {
		t.Errorf("tools = %+v, want the canned single tool 'a'", parsed.Result.Tools)
	}
}

func TestServer_NonLoopbackOriginForbidden(t *testing.T) {
	ts := newTestServer(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Origin", "http://attacker.evil")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestServer_NotificationReturns202(t *testing.T) {
	ts := newTestServer(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","method":"notifications/log","params":{"msg":"hi"}}`
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Ported "spine" tests.
//
// These are the streamable-HTTP behavioral tests originally living in
// cmd/clown-stdio-bridge/http_test.go, where they exercised the bridge's
// *translator over an in-memory pipe pair. The translator lives in package
// main and is not importable here, so the pipe-backed child simulator is
// replaced with fakeChild — an in-package RequestHandler that emulates the
// exact translator contract the tests depend on (request/response matching by
// id, fire-and-forget notifications, and a broadcast channel of
// server-initiated messages). Every assertion is preserved verbatim.
// ---------------------------------------------------------------------------

// fakeChild is a scriptable RequestHandler standing in for the bridge's
// pipe-backed *translator. onRequest, if set, is invoked in a goroutine per
// SendRequest so the test's script can drive the child's timing and output via
// reply/broadcast.
type fakeChild struct {
	mu      sync.Mutex
	pending map[string]chan json.RawMessage
	subs    map[chan json.RawMessage]struct{}
	// onRequest is called (in a goroutine) for each SendRequest with the idKey
	// and body; the script uses c.reply(idKey, respJSON) to deliver the response
	// and c.broadcast(notifJSON) to push a server-initiated message.
	onRequest func(c *fakeChild, idKey string, body []byte)
}

func newFakeChild() *fakeChild {
	return &fakeChild{
		pending: make(map[string]chan json.RawMessage),
		subs:    make(map[chan json.RawMessage]struct{}),
	}
}

// SendRequest registers a buffered waiter under idKey, launches the scripted
// onRequest (if any), then blocks until reply delivers a response or ctx is
// canceled.
func (c *fakeChild) SendRequest(ctx context.Context, idKey string, body []byte) (json.RawMessage, error) {
	waiter := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.pending[idKey] = waiter
	c.mu.Unlock()

	if c.onRequest != nil {
		go c.onRequest(c, idKey, body)
	}

	select {
	case resp := <-waiter:
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, idKey)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// SendNotification is fire-and-forget.
func (c *fakeChild) SendNotification(body []byte) error { return nil }

// Subscribe registers a buffered subscriber channel and returns it with a
// cancel that unregisters it (safe to call once).
func (c *fakeChild) Subscribe() (<-chan json.RawMessage, func()) {
	ch := make(chan json.RawMessage, 256)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

// reply delivers resp to the SendRequest blocked on idKey.
func (c *fakeChild) reply(idKey string, resp json.RawMessage) {
	c.mu.Lock()
	waiter, ok := c.pending[idKey]
	delete(c.pending, idKey)
	c.mu.Unlock()
	if ok {
		waiter <- resp // buffered size 1, non-blocking
	}
}

// broadcast pushes msg to every SSE subscriber, dropping if a channel is full.
func (c *fakeChild) broadcast(msg json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ch := range c.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

// idFromBody extracts the raw "id" value from a JSON-RPC request body, mirroring
// the original child simulator's msg["id"] passthrough.
func idFromBody(body []byte) any {
	var msg map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(body), &msg); err != nil {
		return nil
	}
	return msg["id"]
}

// newSpineServer wires a Server over child with the bridge's log prefix so any
// prefix-derived strings match the originals.
func newSpineServer(child *fakeChild, hb Heartbeat) *httptest.Server {
	srv := NewServer(Config{
		Handler:   child,
		Logger:    nullLogger{},
		LogPrefix: "clown-stdio-bridge",
		Heartbeat: hb,
	})
	return httptest.NewServer(http.HandlerFunc(srv.HandleMCP))
}

// echoOnRequest replies immediately with a result echoing the request id.
func echoOnRequest(c *fakeChild, idKey string, body []byte) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      idFromBody(body),
		"result":  map[string]any{"echoed": true},
	}
	out, _ := json.Marshal(resp)
	c.reply(idKey, out)
}

func TestServer_PostRequestReturnsResponse(t *testing.T) {
	child := newFakeChild()
	child.onRequest = echoOnRequest
	srv := newSpineServer(child, Heartbeat{}) // synchronous JSON path
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	got, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, got)
	}
	if id, _ := out["id"].(float64); id != 1 {
		t.Errorf("response id = %v, want 1", out["id"])
	}
}

func TestServer_PostRequestReturnsSSEByDefault(t *testing.T) {
	// Default cadence → SSE response.
	child := newFakeChild()
	child.onRequest = echoOnRequest
	srv := newSpineServer(child, Heartbeat{Streaming: true, Interval: 30 * time.Second})
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(got, []byte("data:")) {
		t.Errorf("SSE body missing data: prefix; got %q", got)
	}
	if !bytes.Contains(got, []byte(`"id":1`)) {
		t.Errorf("SSE body missing response id; got %q", got)
	}
}

// TestServer_PostStreamingHeartbeatProgressToken verifies that when the
// request includes a progressToken, slow responses are kept alive by
// notifications/progress events referencing that token. This is the
// resetTimeoutOnProgress hook path.
func TestServer_PostStreamingHeartbeatProgressToken(t *testing.T) {
	child := newFakeChild()
	// Slow child: wait 80 ms (≥4 heartbeat intervals), then echo response.
	child.onRequest = func(c *fakeChild, idKey string, body []byte) {
		time.Sleep(80 * time.Millisecond)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      idFromBody(body),
			"result":  map[string]any{"slow": true},
		}
		out, _ := json.Marshal(resp)
		c.reply(idKey, out)
	}
	srv := newSpineServer(child, Heartbeat{Streaming: true, Interval: 20 * time.Millisecond})
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":7,"method":"slow","params":{"_meta":{"progressToken":"tok-7"}}}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(got, []byte(`"method":"notifications/progress"`)) {
		t.Errorf("expected at least one notifications/progress event; got %q", got)
	}
	if !bytes.Contains(got, []byte(`"progressToken":"tok-7"`)) {
		t.Errorf("expected progressToken \"tok-7\" echoed in heartbeat; got %q", got)
	}
	if !bytes.Contains(got, []byte(`"id":7`)) {
		t.Errorf("expected final response id=7 on the SSE stream; got %q", got)
	}
}

// TestServer_PostStreamingHeartbeatNoProgressToken verifies the fallback
// path: requests without a progressToken still get keep-alive activity,
// but as SSE comments rather than notifications/progress (per spec —
// progress notifications MUST reference a token from the request).
func TestServer_PostStreamingHeartbeatNoProgressToken(t *testing.T) {
	child := newFakeChild()
	child.onRequest = func(c *fakeChild, idKey string, body []byte) {
		time.Sleep(80 * time.Millisecond)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      idFromBody(body),
			"result":  map[string]any{"slow": true},
		}
		out, _ := json.Marshal(resp)
		c.reply(idKey, out)
	}
	srv := newSpineServer(child, Heartbeat{Streaming: true, Interval: 20 * time.Millisecond})
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":8,"method":"slow"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if bytes.Contains(got, []byte(`"method":"notifications/progress"`)) {
		t.Errorf("did not expect notifications/progress without a token; got %q", got)
	}
	if !bytes.Contains(got, []byte(": heartbeat")) {
		t.Errorf("expected SSE comment heartbeat; got %q", got)
	}
	if !bytes.Contains(got, []byte(`"id":8`)) {
		t.Errorf("expected final response id=8; got %q", got)
	}
}

// TestServer_PostStreamingForwardOnlySuppressesTimer verifies the
// forward-only heartbeat mode: SSE streaming stays on (so child
// notifications and the final response are delivered) but the bridge
// emits NO heartbeats of its own — even though the configured interval
// is short enough to fire several times during the slow child call.
func TestServer_PostStreamingForwardOnlySuppressesTimer(t *testing.T) {
	// 20ms interval would fire ~4 times across the 80ms child wait if the
	// timer were active; forward-only must suppress it entirely.
	child := newFakeChild()
	// Slow child: wait 80 ms (≥4 heartbeat intervals) then echo.
	child.onRequest = func(c *fakeChild, idKey string, body []byte) {
		time.Sleep(80 * time.Millisecond)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      idFromBody(body),
			"result":  map[string]any{"slow": true},
		}
		out, _ := json.Marshal(resp)
		c.reply(idKey, out)
	}
	srv := newSpineServer(child, Heartbeat{Streaming: true, Interval: 0, Fallback: false})
	defer srv.Close()

	// Includes a progressToken: in the default regime this would produce
	// notifications/progress heartbeats; forward-only must not.
	body := `{"jsonrpc":"2.0","id":9,"method":"slow","params":{"_meta":{"progressToken":"tok-9"}}}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream (streaming must stay on)", got)
	}
	got, _ := io.ReadAll(resp.Body)
	if bytes.Contains(got, []byte(`"method":"notifications/progress"`)) {
		t.Errorf("forward-only must not emit bridge progress heartbeats; got %q", got)
	}
	if bytes.Contains(got, []byte(": heartbeat")) {
		t.Errorf("forward-only must not emit SSE comment heartbeats; got %q", got)
	}
	if !bytes.Contains(got, []byte(`"id":9`)) {
		t.Errorf("expected final response id=9 on the SSE stream; got %q", got)
	}
}

// TestServer_PostStreamingFallbackHeartbeatOnSilence verifies the
// activity-gated fallback mode (clown#109): with a silent child, the bridge
// fires at least one heartbeat once the silence threshold elapses — unlike
// plain forward-only, which would emit none.
func TestServer_PostStreamingFallbackHeartbeatOnSilence(t *testing.T) {
	// 20ms silence threshold; child stays silent 80ms (≥4 thresholds) before
	// responding, so the fallback timer must fire at least once.
	child := newFakeChild()
	// Silent child: wait 80ms with no output, then echo.
	child.onRequest = func(c *fakeChild, idKey string, body []byte) {
		time.Sleep(80 * time.Millisecond)
		resp := map[string]any{"jsonrpc": "2.0", "id": idFromBody(body), "result": map[string]any{"ok": true}}
		out, _ := json.Marshal(resp)
		c.reply(idKey, out)
	}
	srv := newSpineServer(child, Heartbeat{Streaming: true, Interval: 20 * time.Millisecond, Fallback: true})
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":11,"method":"slow"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(got, []byte(": heartbeat")) {
		t.Errorf("fallback must emit a heartbeat after the silence threshold; got %q", got)
	}
	if !bytes.Contains(got, []byte(`"id":11`)) {
		t.Errorf("expected final response id=11; got %q", got)
	}
}

// TestServer_PostStreamingFallbackResetByActivity verifies that steady child
// output re-arms the fallback timer (clown#109): the child emits a
// notification every 10ms — well under the 50ms threshold — for ~120ms before
// responding, so the silence window never elapses and NO heartbeat is emitted
// despite the total wait exceeding the threshold many times over.
func TestServer_PostStreamingFallbackResetByActivity(t *testing.T) {
	child := newFakeChild()
	// Active child: emit a notification every 10ms for ~120ms (each well under
	// the 50ms threshold), then respond. Notifications carry a method, so the
	// translator routes them to the broadcast the POST handler observes.
	child.onRequest = func(c *fakeChild, idKey string, body []byte) {
		for i := 0; i < 12; i++ {
			time.Sleep(10 * time.Millisecond)
			c.broadcast(json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"tick"}}`))
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": idFromBody(body), "result": map[string]any{"ok": true}}
		out, _ := json.Marshal(resp)
		c.reply(idKey, out)
	}
	srv := newSpineServer(child, Heartbeat{Streaming: true, Interval: 50 * time.Millisecond, Fallback: true})
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":12,"method":"slow"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if bytes.Contains(got, []byte(": heartbeat")) {
		t.Errorf("steady child activity must keep re-arming the fallback timer (no heartbeat); got %q", got)
	}
	if !bytes.Contains(got, []byte(`"id":12`)) {
		t.Errorf("expected final response id=12; got %q", got)
	}
}

func TestServer_PostInvalidJSONReturns400(t *testing.T) {
	child := newFakeChild()
	srv := newSpineServer(child, Heartbeat{})
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServer_DeleteReturns405(t *testing.T) {
	child := newFakeChild()
	srv := newSpineServer(child, Heartbeat{})
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestServer_OriginValidation(t *testing.T) {
	child := newFakeChild()
	child.onRequest = echoOnRequest
	// Origin checks run before dispatch; keep the synchronous JSON path
	// (Heartbeat{}) so the allowed cases return cleanly.
	srv := newSpineServer(child, Heartbeat{})
	defer srv.Close()

	tests := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},                                 // no origin (curl)
		{"http://127.0.0.1:8080", http.StatusOK},            // loopback
		{"http://localhost", http.StatusOK},                 // loopback
		{"https://localhost:8443", http.StatusOK},           // loopback
		{"http://example.com", http.StatusForbidden},        // remote
		{"http://attacker.evil", http.StatusForbidden},      // remote
		{"http://127.0.0.1.evil.com", http.StatusForbidden}, // sneaky
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("origin %q: status = %d, want %d", tt.origin, resp.StatusCode, tt.want)
			}
		})
	}
}

func TestServer_GetSSEStream(t *testing.T) {
	child := newFakeChild()
	srv := newSpineServer(child, Heartbeat{})
	defer srv.Close()

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer clientCancel()
	req, _ := http.NewRequestWithContext(clientCtx, http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	// Push a server-initiated notification through the child's broadcast;
	// expect it to appear on the SSE stream.
	notif := json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	go func() {
		// Wait briefly for the SSE subscriber to register before
		// emitting; broadcast to a not-yet-subscribed listener is
		// dropped silently.
		time.Sleep(50 * time.Millisecond)
		child.broadcast(notif)
	}()

	// Read in a goroutine so we can race it against a deadline. The
	// request context (clientCtx) will close the body when it expires,
	// terminating the read.
	type readResult struct {
		got []byte
		err error
	}
	readCh := make(chan readResult, 1)
	go func() {
		var got []byte
		buf := make([]byte, 1024)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				got = append(got, buf[:n]...)
				if bytes.Contains(got, []byte("tools/list_changed")) {
					readCh <- readResult{got: got}
					return
				}
			}
			if err != nil {
				readCh <- readResult{got: got, err: err}
				return
			}
		}
	}()

	select {
	case res := <-readCh:
		if !bytes.Contains(res.got, []byte("tools/list_changed")) {
			t.Errorf("did not receive expected SSE event; got %q (err=%v)", res.got, res.err)
		}
	case <-time.After(time.Second):
		t.Errorf("SSE read timed out")
	}
}

// TestNewServer_FallbackWithoutIntervalCoercedAndLogged verifies the
// construction-time guard: a Heartbeat asking for the activity-gated fallback
// but with a non-positive Interval (which could never arm the timer) is coerced
// to plain forward-only and a warning is logged, so the misconfiguration is not
// silent.
func TestNewServer_FallbackWithoutIntervalCoercedAndLogged(t *testing.T) {
	log := &recordingLogger{}
	srv := NewServer(Config{
		Handler:   stubHandler{},
		Logger:    log,
		Heartbeat: Heartbeat{Streaming: true, Fallback: true, Interval: 0},
	})
	if srv.heartbeat.Fallback {
		t.Errorf("Fallback should have been coerced off; got %+v", srv.heartbeat)
	}
	if !srv.heartbeat.Streaming {
		t.Errorf("Streaming must be preserved (forward-only); got %+v", srv.heartbeat)
	}
	if !strings.Contains(log.joined(), "forward-only") {
		t.Errorf("expected a coercion warning mentioning forward-only; got %q", log.joined())
	}
}

// TestNewServer_ValidFallbackNotCoerced confirms the guard leaves a correctly
// configured activity-gated fallback (Interval > 0) untouched.
func TestNewServer_ValidFallbackNotCoerced(t *testing.T) {
	log := &recordingLogger{}
	srv := NewServer(Config{
		Handler:   stubHandler{},
		Logger:    log,
		Heartbeat: Heartbeat{Streaming: true, Fallback: true, Interval: 20 * time.Millisecond},
	})
	if !srv.heartbeat.Fallback || srv.heartbeat.Interval != 20*time.Millisecond {
		t.Errorf("valid fallback policy was altered; got %+v", srv.heartbeat)
	}
	if log.joined() != "" {
		t.Errorf("no warning expected for a valid fallback; got %q", log.joined())
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// runTranslator sets up a translator over an in-memory pipe pair, with
// a goroutine that simulates the wrapped child's responses by reading
// stdin and emitting echo responses on stdoutW. Returns the translator
// and a cleanup func.
func runTranslator(t *testing.T) (*translator, func()) {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = tr.Run(ctx) }()

	// Wrapped-child simulator: read each line from stdin, parse it,
	// echo a matching response on stdout. Notifications and responses
	// from the client are silently consumed (no-op).
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := stdinR.Read(tmp)
			if err != nil {
				return
			}
			buf = append(buf, tmp[:n]...)
			for {
				idx := bytes.IndexByte(buf, '\n')
				if idx < 0 {
					break
				}
				line := buf[:idx]
				buf = buf[idx+1:]
				var msg map[string]any
				if err := json.Unmarshal(line, &msg); err != nil {
					continue
				}
				if _, hasMethod := msg["method"]; hasMethod {
					if _, hasID := msg["id"]; hasID {
						resp := map[string]any{
							"jsonrpc": "2.0",
							"id":      msg["id"],
							"result":  map[string]any{"echoed_method": msg["method"]},
						}
						out, _ := json.Marshal(resp)
						_, _ = stdoutW.Write(append(out, '\n'))
					}
				}
			}
		}
	}()

	cleanup := func() {
		cancel()
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}
	return tr, cleanup
}

func TestHTTP_PostRequestReturnsResponse(t *testing.T) {
	t.Setenv(heartbeatEnvVar, "0") // exercise the legacy synchronous JSON path
	tr, cleanup := runTranslator(t)
	defer cleanup()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

func TestHTTP_PostRequestReturnsSSEByDefault(t *testing.T) {
	// Heartbeat env var unset → default cadence → SSE response.
	tr, cleanup := runTranslator(t)
	defer cleanup()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

// TestHTTP_PostStreamingHeartbeatProgressToken verifies that when the
// request includes a progressToken, slow responses are kept alive by
// notifications/progress events referencing that token. This is the
// resetTimeoutOnProgress hook path.
func TestHTTP_PostStreamingHeartbeatProgressToken(t *testing.T) {
	t.Setenv(heartbeatEnvVar, "20ms")

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()
	defer func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}()

	// Slow wrapped child: read request, wait 80 ms (≥4 heartbeat
	// intervals), then echo response.
	go func() {
		buf := make([]byte, 4096)
		n, err := stdinR.Read(buf)
		if err != nil {
			return
		}
		var msg map[string]any
		if jerr := json.Unmarshal(bytes.TrimSpace(buf[:n]), &msg); jerr != nil {
			return
		}
		time.Sleep(80 * time.Millisecond)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      msg["id"],
			"result":  map[string]any{"slow": true},
		}
		out, _ := json.Marshal(resp)
		_, _ = stdoutW.Write(append(out, '\n'))
	}()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

// TestHTTP_PostStreamingHeartbeatNoProgressToken verifies the fallback
// path: requests without a progressToken still get keep-alive activity,
// but as SSE comments rather than notifications/progress (per spec —
// progress notifications MUST reference a token from the request).
func TestHTTP_PostStreamingHeartbeatNoProgressToken(t *testing.T) {
	t.Setenv(heartbeatEnvVar, "20ms")

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()
	defer func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}()

	go func() {
		buf := make([]byte, 4096)
		n, err := stdinR.Read(buf)
		if err != nil {
			return
		}
		var msg map[string]any
		if jerr := json.Unmarshal(bytes.TrimSpace(buf[:n]), &msg); jerr != nil {
			return
		}
		time.Sleep(80 * time.Millisecond)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      msg["id"],
			"result":  map[string]any{"slow": true},
		}
		out, _ := json.Marshal(resp)
		_, _ = stdoutW.Write(append(out, '\n'))
	}()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

// TestHTTP_PostStreamingForwardOnlySuppressesTimer verifies the
// forward-only heartbeat mode: SSE streaming stays on (so child
// notifications and the final response are delivered) but the bridge
// emits NO heartbeats of its own — even though the configured interval
// is short enough to fire several times during the slow child call.
func TestHTTP_PostStreamingForwardOnlySuppressesTimer(t *testing.T) {
	// 20ms interval would fire ~4 times across the 80ms child wait if the
	// timer were active; forward-only must suppress it entirely.
	t.Setenv(heartbeatEnvVar, "20ms")
	t.Setenv(heartbeatModeEnvVar, "forward-only")

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()
	defer func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}()

	// Slow wrapped child: wait 80 ms (≥4 heartbeat intervals) then echo.
	go func() {
		buf := make([]byte, 4096)
		n, err := stdinR.Read(buf)
		if err != nil {
			return
		}
		var msg map[string]any
		if jerr := json.Unmarshal(bytes.TrimSpace(buf[:n]), &msg); jerr != nil {
			return
		}
		time.Sleep(80 * time.Millisecond)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      msg["id"],
			"result":  map[string]any{"slow": true},
		}
		out, _ := json.Marshal(resp)
		_, _ = stdoutW.Write(append(out, '\n'))
	}()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

// setOrUnsetEnv sets (or unsets) an env var for the duration of the test,
// restoring the original state on cleanup. Unlike t.Setenv it can model an
// absent variable, which heartbeatMode distinguishes from an empty value.
func setOrUnsetEnv(t *testing.T, key, val string, set bool) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, orig)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	if set {
		_ = os.Setenv(key, val)
	} else {
		_ = os.Unsetenv(key)
	}
}

func TestHeartbeatMode(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		modeSet      bool
		interval     string
		intervalSet  bool
		wantStream   bool
		wantInterval time.Duration
	}{
		{name: "default: stream at default cadence", wantStream: true, wantInterval: heartbeatDefault},
		{name: "explicit interval", interval: "5s", intervalSet: true, wantStream: true, wantInterval: 5 * time.Second},
		{name: "interval off disables streaming", interval: "off", intervalSet: true, wantStream: false, wantInterval: 0},
		{name: "forward-only streams without timer", mode: "forward-only", modeSet: true, wantStream: true, wantInterval: 0},
		{name: "child alias", mode: "child", modeSet: true, wantStream: true, wantInterval: 0},
		{name: "forward-only is case/space insensitive", mode: "  Forward-Only  ", modeSet: true, wantStream: true, wantInterval: 0},
		{name: "forward-only overrides a short interval", mode: "forward-only", modeSet: true, interval: "20ms", intervalSet: true, wantStream: true, wantInterval: 0},
		{name: "unknown mode falls back to interval", mode: "bogus", modeSet: true, interval: "5s", intervalSet: true, wantStream: true, wantInterval: 5 * time.Second},
		{name: "unknown mode falls back to default", mode: "bogus", modeSet: true, wantStream: true, wantInterval: heartbeatDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOrUnsetEnv(t, heartbeatModeEnvVar, tt.mode, tt.modeSet)
			setOrUnsetEnv(t, heartbeatEnvVar, tt.interval, tt.intervalSet)
			gotStream, gotInterval := heartbeatMode()
			if gotStream != tt.wantStream {
				t.Errorf("streaming = %v, want %v", gotStream, tt.wantStream)
			}
			if gotInterval != tt.wantInterval {
				t.Errorf("interval = %v, want %v", gotInterval, tt.wantInterval)
			}
		})
	}
}

func TestHTTP_PostNotificationReturns202(t *testing.T) {
	tr, cleanup := runTranslator(t)
	defer cleanup()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
	defer srv.Close()

	body := `{"jsonrpc":"2.0","method":"notifications/log","params":{"msg":"hi"}}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
}

func TestHTTP_PostInvalidJSONReturns400(t *testing.T) {
	tr, cleanup := runTranslator(t)
	defer cleanup()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

func TestHTTP_DeleteReturns405(t *testing.T) {
	tr, cleanup := runTranslator(t)
	defer cleanup()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

func TestHTTP_OriginValidation(t *testing.T) {
	tr, cleanup := runTranslator(t)
	defer cleanup()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

func TestHTTP_GetSSEStream(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()
	defer func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}()

	h := &httpHandler{t: tr, logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
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

	// Push a server-initiated notification through the wrapped child's
	// stdout; expect it to appear on the SSE stream.
	notif := []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	go func() {
		// Wait briefly for the SSE subscriber to register before
		// emitting; broadcast to a not-yet-subscribed listener is
		// dropped silently.
		time.Sleep(50 * time.Millisecond)
		_, _ = stdoutW.Write(append(notif, '\n'))
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

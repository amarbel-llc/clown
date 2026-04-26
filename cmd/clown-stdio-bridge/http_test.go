package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	got, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, got)
	}
	if id, _ := out["id"].(float64); id != 1 {
		t.Errorf("response id = %v, want 1", out["id"])
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
		{"", http.StatusOK},                                     // no origin (curl)
		{"http://127.0.0.1:8080", http.StatusOK},                // loopback
		{"http://localhost", http.StatusOK},                     // loopback
		{"https://localhost:8443", http.StatusOK},               // loopback
		{"http://example.com", http.StatusForbidden},            // remote
		{"http://attacker.evil", http.StatusForbidden},          // remote
		{"http://127.0.0.1.evil.com", http.StatusForbidden},     // sneaky
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

package jugglertools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeServer answers MCP JSON-RPC by method. sse wraps bodies in an event
// stream. It records the session header seen on non-initialize calls.
type fakeServer struct {
	sse         bool
	toolsResult string
	callResult  string
	sessions    []string
}

func (f *fakeServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			t.Errorf("bad request: %v", err)
		}
		var result string
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = `{"protocolVersion":"2025-06-18","capabilities":{}}`
		case "tools/list":
			f.sessions = append(f.sessions, r.Header.Get("Mcp-Session-Id"))
			result = f.toolsResult
		case "tools/call":
			f.sessions = append(f.sessions, r.Header.Get("Mcp-Session-Id"))
			result = f.callResult
		}
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
		if f.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"progress\":1}\n\ndata: %s\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func TestListToolsKindExtraction(t *testing.T) {
	f := &fakeServer{toolsResult: `{"tools":[
		{"name":"a","description":"da","inputSchema":{"type":"object"},"_meta":{"kind":"issue"}},
		{"name":"b","annotations":{"kind":"note"}},
		{"name":"c","inputSchema":{}},
		{"name":"d","_meta":{"kind":"x"},"annotations":{"kind":"y"}},
		{"name":""}
	]}`}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "issue", "b": "note", "c": "", "d": "x"}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d: %+v", len(tools), len(want), tools)
	}
	for _, tl := range tools {
		if tl.Kind != want[tl.Name] {
			t.Errorf("tool %s kind = %q, want %q", tl.Name, tl.Kind, want[tl.Name])
		}
	}
	if tools[0].Description != "da" || string(tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("tool a fields wrong: %+v", tools[0])
	}
	if f.sessions[0] != "sess-1" {
		t.Errorf("session header on tools/list = %q, want sess-1", f.sessions[0])
	}
}

func TestCallToolHappyAndIsError(t *testing.T) {
	f := &fakeServer{callResult: `{"content":[{"type":"text","text":"ok"}],"isError":false}`}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	content, isErr, err := c.Call(ctx, "t", json.RawMessage(`{"x":1}`))
	if err != nil || isErr {
		t.Fatalf("err=%v isErr=%v", err, isErr)
	}
	if string(content) != `[{"type":"text","text":"ok"}]` {
		t.Errorf("content = %s", content)
	}
	if f.sessions[0] != "sess-1" {
		t.Errorf("session header = %q", f.sessions[0])
	}

	f.callResult = `{"content":[{"type":"text","text":"permission denied"}],"isError":true}`
	content, isErr, err = c.Call(ctx, "t", nil)
	if err != nil {
		t.Fatalf("tool-level error must not be a transport error: %v", err)
	}
	if !isErr || !strings.Contains(string(content), "permission denied") {
		t.Errorf("isErr=%v content=%s", isErr, content)
	}
}

func TestCallToolStructuredContent(t *testing.T) {
	f := &fakeServer{callResult: `{"content":[{"type":"text","text":"Filed #7"}],"structuredContent":{"uri":"https://f/7"},"isError":false}`}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	content, isErr, err := c.Call(ctx, "t", nil)
	if err != nil || isErr || string(content) != `{"content":[{"type":"text","text":"Filed #7"}],"structuredContent":{"uri":"https://f/7"}}` {
		t.Fatalf("content = %s, isErr = %v, err = %v", content, isErr, err)
	}
	f.callResult = `{"structuredContent":{"uri":"https://f/8"}}`
	if content, _, _ = c.Call(ctx, "t", nil); string(content) != `{"content":[],"structuredContent":{"uri":"https://f/8"}}` {
		t.Errorf("structured-only content = %s", content)
	}
}

func TestCallToolSSE(t *testing.T) {
	f := &fakeServer{sse: true, callResult: `{"content":[{"type":"text","text":"sse"}]}`}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	content, isErr, err := c.Call(ctx, "t", nil)
	if err != nil || isErr {
		t.Fatalf("err=%v isErr=%v", err, isErr)
	}
	if !strings.Contains(string(content), "sse") {
		t.Errorf("content = %s", content)
	}
}

func TestTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New(srv.URL)
	if _, _, err := c.Call(context.Background(), "t", nil); err == nil {
		t.Error("expected transport error on 500")
	}

	// JSON-RPC error envelope is a protocol error, not isError.
	rpcErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`)
	}))
	defer rpcErr.Close()
	_, isErr, err := New(rpcErr.URL).Call(context.Background(), "t", nil)
	if err == nil || isErr || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err=%v isErr=%v", err, isErr)
	}

	// Invalid params is the model's fault: a tool-level error it can see.
	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"bad arguments"}}`)
	}))
	defer invalid.Close()
	content, isErr, err := New(invalid.URL).Call(context.Background(), "t", nil)
	if err != nil || !isErr || !strings.Contains(string(content), "bad arguments") {
		t.Errorf("invalid params: content=%s err=%v isErr=%v", content, err, isErr)
	}

	// Unreachable server.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	if _, err := New(url).ListTools(context.Background()); err == nil {
		t.Error("expected dial error")
	}
}

func TestCallTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer srv.Close()
	c := New(srv.URL, WithCallTimeout(50*time.Millisecond))
	start := time.Now()
	if _, _, err := c.Call(context.Background(), "t", nil); err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("timeout not honoured")
	}
}

func TestOversizedResponse(t *testing.T) {
	big := strings.Repeat("a", 2048)
	f := &fakeServer{callResult: fmt.Sprintf(`{"content":[{"type":"text","text":%q}]}`, big)}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := New(srv.URL, WithMaxResponseBytes(512))
	_, _, err := c.Call(context.Background(), "t", nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

func TestExtractKind(t *testing.T) {
	cases := []struct{ meta, ann, want string }{
		{`{"kind":"issue"}`, ``, "issue"},
		{``, `{"kind":"note"}`, "note"},
		{`{"kind":""}`, `{"kind":"note"}`, "note"},
		{`{"other":1}`, `{"readOnlyHint":true}`, ""},
		{`not json`, ``, ""},
		{``, ``, ""},
	}
	for _, tc := range cases {
		if got := ExtractKind(json.RawMessage(tc.meta), json.RawMessage(tc.ann)); got != tc.want {
			t.Errorf("ExtractKind(%q,%q) = %q, want %q", tc.meta, tc.ann, got, tc.want)
		}
	}
}

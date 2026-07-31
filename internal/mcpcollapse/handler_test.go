package mcpcollapse

import (
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

	"code.linenisgreat.com/clown/internal/mcphttp"
)

// callableUpstream extends the enumeration-only fake with a tools/call handler
// that records what it was asked to run, so a test can assert both that the
// aggregator dispatched (name + arguments) and what it handed back verbatim.
type callableUpstream struct {
	tools []fakeTool
	// callResult is the raw JSON-RPC "result" object returned from tools/call.
	// When empty a default text result is used.
	callResult string
	// callError, when set, makes tools/call return a JSON-RPC error envelope
	// (a TRANSPORT/protocol failure the aggregator surfaces) rather than a
	// result. This is distinct from a tool that RAN and returned an isError
	// result (that goes in callResult).
	callError string
	// sessionAware, when true, makes initialize mint a fresh session id (echoed
	// via Mcp-Session-Id) and makes tools/call REJECT any request whose
	// Mcp-Session-Id does not match the currently-valid id — the shape the
	// stale-session retry recovers from. rotateSession() invalidates the current
	// id so the next tools/call on the old id fails until a fresh initialize.
	sessionAware bool
	// alwaysRejectCall, when true, makes EVERY tools/call 404 regardless of
	// session — even one carrying a freshly-initialized id. It models an upstream
	// that can never satisfy the call, so the handler's single retry cannot
	// recover: used to prove the retry is bounded to exactly one attempt.
	alwaysRejectCall bool

	mu sync.Mutex
	// gotCall records whether tools/call was invoked at all.
	gotCall bool
	// callCount counts tools/call invocations (including rejected ones).
	callCount int
	// gotName / gotArgs record the params of the last tools/call.
	gotName string
	gotArgs json.RawMessage
	// callBodies records the raw request body of every tools/call, so a test can
	// assert the exact bytes the upstream received (JSON-escaping regression).
	callBodies [][]byte
	// validSession is the currently-accepted session id (sessionAware mode).
	validSession string
	// sessionSeq mints monotonically-distinct session ids across initializes.
	sessionSeq int
}

func (f *callableUpstream) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(bodyBytes, &envelope)

		switch envelope.Method {
		case "initialize":
			if f.sessionAware {
				f.mu.Lock()
				f.sessionSeq++
				f.validSession = fmt.Sprintf("sess-%d", f.sessionSeq)
				current := f.validSession
				f.mu.Unlock()
				w.Header().Set(mcphttp.MCPSessionIDHeader, current)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(envelope.ID) + `,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake","version":"1"}}}`))
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(envelope.ID) + `,"result":{"tools":[` + f.toolsJSON() + `]}}`))
		case "tools/call":
			f.mu.Lock()
			f.callCount++
			f.callBodies = append(f.callBodies, bodyBytes)
			reject := f.alwaysRejectCall ||
				(f.sessionAware && r.Header.Get(mcphttp.MCPSessionIDHeader) != f.validSession)
			// A mismatched/stale (or always-rejected) id is rejected BEFORE the
			// call is recorded as a real dispatch, so a test can assert that the
			// rejected attempt never "ran" the tool.
			if reject {
				f.mu.Unlock()
				http.Error(w, "unknown or expired session", http.StatusNotFound)
				return
			}
			f.gotCall = true
			f.gotName = envelope.Params.Name
			f.gotArgs = envelope.Params.Arguments
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if f.callError != "" {
				w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(envelope.ID) + `,"error":{"code":-32000,"message":` + jsonString(f.callError) + `}}`))
				return
			}
			result := f.callResult
			if result == "" {
				result = `{"content":[{"type":"text","text":"ok"}]}`
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(envelope.ID) + `,"result":` + result + `}`))
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}
}

// rotateSession invalidates the currently-valid session id so the next
// tools/call carrying the old id is rejected (until a fresh initialize),
// simulating an upstream that restarted or TTL'd out its session.
func (f *callableUpstream) rotateSession() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validSession = ""
}

// callTally returns the number of tools/call requests received (including
// rejected stale ones) and how many actually ran the tool.
func (f *callableUpstream) callTally() (received int, ran bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount, f.gotCall
}

// lastCallBody returns the raw request body of the most recent tools/call.
func (f *callableUpstream) lastCallBody() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.callBodies) == 0 {
		return nil
	}
	return f.callBodies[len(f.callBodies)-1]
}

func (f *callableUpstream) toolsJSON() string {
	parts := make([]string, 0, len(f.tools))
	for _, t := range f.tools {
		schema := t.schema
		if schema == "" {
			schema = "{}"
		}
		parts = append(parts, `{"name":`+jsonString(t.name)+`,"description":`+jsonString(t.description)+`,"inputSchema":`+schema+`}`)
	}
	return strings.Join(parts, ",")
}

func (f *callableUpstream) called() (bool, string, json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotCall, f.gotName, f.gotArgs
}

// buildHandler stands up one callableUpstream, builds a real Aggregator over it,
// and returns a Handler plus the server so a test can assert dispatch. The
// upstream name is "srv".
func buildHandler(t *testing.T, up *callableUpstream) (*Handler, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(up.handler())
	agg, err := NewAggregator(context.Background(), []Upstream{
		{Name: "srv", URL: srv.URL},
	}, time.Second)
	if err != nil {
		srv.Close()
		t.Fatalf("NewAggregator: %v", err)
	}
	return NewHandler(agg), srv
}

// callVerb drives one tools/call through the Handler and returns the parsed
// result object (json.RawMessage) plus whether the top-level response carried a
// JSON-RPC error. It fails the test on transport/parse errors.
func callVerb(t *testing.T, h *Handler, name string, arguments any) (json.RawMessage, bool) {
	t.Helper()
	args, _ := json.Marshal(arguments)
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "1",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": json.RawMessage(args),
		},
	})
	resp, err := h.SendRequest(context.Background(), `"1"`, body)
	if err != nil {
		t.Fatalf("SendRequest(%s): %v", name, err)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, resp)
	}
	hasErr := len(env.Error) > 0 && string(env.Error) != "null"
	return env.Result, hasErr
}

// resultIsError reports whether an MCP CallToolResult (the json in a tools/call
// result) has isError:true — the shape the aggregator uses to signal an
// aggregator-originated failure through the verb result.
func resultIsError(t *testing.T, result json.RawMessage) bool {
	t.Helper()
	var r struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		t.Fatalf("unmarshal result: %v (%s)", err, result)
	}
	return r.IsError
}

// resultText concatenates the text of every text content block in a
// CallToolResult, so a test can assert on the human-readable payload.
func resultText(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		t.Fatalf("unmarshal result content: %v (%s)", err, result)
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// TestToolsListReturnsExactlyThreeVerbs: tools/list through the Handler returns
// EXACTLY mcp_list/mcp_describe/mcp_call, each with an inputSchema. This is the
// collapse — the harness never sees the N upstream tools.
func TestToolsListReturnsExactlyThreeVerbs(t *testing.T) {
	up := &callableUpstream{tools: []fakeTool{
		{name: "commit", description: "make a commit"},
		{name: "status", description: "show status"},
	}}
	h, srv := buildHandler(t, up)
	defer srv.Close()

	body := []byte(`{"jsonrpc":"2.0","id":"1","method":"tools/list","params":{}}`)
	resp, err := h.SendRequest(context.Background(), `"1"`, body)
	if err != nil {
		t.Fatalf("SendRequest tools/list: %v", err)
	}
	var parsed struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal tools/list: %v (%s)", err, resp)
	}
	got := parsed.Result.Tools
	if len(got) != 3 {
		t.Fatalf("tools/list returned %d tools, want exactly 3: %s", len(got), resp)
	}
	want := map[string]bool{"mcp_list": false, "mcp_describe": false, "mcp_call": false}
	for _, tool := range got {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected verb %q in tools/list", tool.Name)
		}
		want[tool.Name] = true
		if len(tool.InputSchema) == 0 {
			t.Fatalf("verb %q has no inputSchema", tool.Name)
		}
		if tool.Description == "" {
			t.Fatalf("verb %q has no description", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("tools/list missing verb %q", name)
		}
	}
}

// TestMCPListEnumeratesGroupedByServerWithFilters: mcp_list lists every
// registered tool (id + description, no schemas), groups by server, honors the
// query and server filters, and surfaces degraded upstreams.
func TestMCPListEnumeratesGroupedByServerWithFilters(t *testing.T) {
	healthy := &callableUpstream{tools: []fakeTool{
		{name: "commit", description: "make a commit"},
		{name: "status", description: "show working tree status"},
	}}
	srvHealthy := httptest.NewServer(healthy.handler())
	defer srvHealthy.Close()
	web := &callableUpstream{tools: []fakeTool{
		{name: "search", description: "search the web"},
	}}
	srvWeb := httptest.NewServer(web.handler())
	defer srvWeb.Close()
	// A broken upstream so mcp_list must surface a degraded entry.
	broken := httptest.NewServer((&fakeUpstream{failToolsList: true}).handler())
	defer broken.Close()

	agg, err := NewAggregator(context.Background(), []Upstream{
		{Name: "grit", URL: srvHealthy.URL},
		{Name: "web", URL: srvWeb.URL},
		{Name: "broken", URL: broken.URL},
	}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}
	h := NewHandler(agg)

	// No filters: every tool id present, and the degraded upstream is named.
	result, isErr := callVerb(t, h, "mcp_list", map[string]any{})
	if isErr {
		t.Fatalf("mcp_list returned an error result: %s", result)
	}
	text := resultText(t, result)
	for _, id := range []string{"grit.commit", "grit.status", "web.search"} {
		if !strings.Contains(text, id) {
			t.Fatalf("mcp_list missing id %q:\n%s", id, text)
		}
	}
	if !strings.Contains(text, "make a commit") {
		t.Fatalf("mcp_list missing a description:\n%s", text)
	}
	if !strings.Contains(text, "broken") {
		t.Fatalf("mcp_list should surface the degraded upstream 'broken':\n%s", text)
	}

	// query filter narrows to matching id/description substrings.
	result, _ = callVerb(t, h, "mcp_list", map[string]any{"query": "search"})
	text = resultText(t, result)
	if !strings.Contains(text, "web.search") {
		t.Fatalf("query=search should keep web.search:\n%s", text)
	}
	if strings.Contains(text, "grit.commit") {
		t.Fatalf("query=search should drop grit.commit:\n%s", text)
	}

	// server filter narrows to exact server name.
	result, _ = callVerb(t, h, "mcp_list", map[string]any{"server": "grit"})
	text = resultText(t, result)
	if !strings.Contains(text, "grit.commit") || !strings.Contains(text, "grit.status") {
		t.Fatalf("server=grit should keep grit tools:\n%s", text)
	}
	if strings.Contains(text, "web.search") {
		t.Fatalf("server=grit should drop web.search:\n%s", text)
	}
}

// TestMCPDescribeKnownAndUnknown: mcp_describe(known id) returns the stored
// schema + description verbatim; mcp_describe(unknown id) returns a shaped error
// result.
func TestMCPDescribeKnownAndUnknown(t *testing.T) {
	up := &callableUpstream{tools: []fakeTool{
		{name: "commit", description: "make a commit", schema: `{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`},
	}}
	h, srv := buildHandler(t, up)
	defer srv.Close()

	result, isErr := callVerb(t, h, "mcp_describe", map[string]any{"tool_id": "srv.commit"})
	if isErr {
		t.Fatalf("mcp_describe(known) returned an error result: %s", result)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "make a commit") {
		t.Fatalf("mcp_describe should include the description:\n%s", text)
	}
	if !strings.Contains(text, `"required":["message"]`) {
		t.Fatalf("mcp_describe should include the stored schema verbatim:\n%s", text)
	}

	result, isErr = callVerb(t, h, "mcp_describe", map[string]any{"tool_id": "srv.nope"})
	if !isErr && !resultIsError(t, result) {
		t.Fatalf("mcp_describe(unknown) should be a shaped error, got: %s", result)
	}
}

// TestMCPCallDispatchesVerbatim: mcp_call(known id, valid args) dispatches to the
// upstream with name=Entry.Tool and the args, and returns the upstream's result
// content untouched.
func TestMCPCallDispatchesVerbatim(t *testing.T) {
	up := &callableUpstream{
		tools: []fakeTool{
			{name: "commit", description: "make a commit", schema: `{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`},
		},
		callResult: `{"content":[{"type":"text","text":"committed abc123"}]}`,
	}
	h, srv := buildHandler(t, up)
	defer srv.Close()

	result, isErr := callVerb(t, h, "mcp_call", map[string]any{
		"tool_id": "srv.commit",
		"args":    map[string]any{"message": "hello"},
	})
	if isErr {
		t.Fatalf("mcp_call returned a JSON-RPC error: %s", result)
	}
	if resultIsError(t, result) {
		t.Fatalf("mcp_call on a valid call should not be an isError result: %s", result)
	}
	if got := resultText(t, result); got != "committed abc123" {
		t.Fatalf("mcp_call result not verbatim: got %q", got)
	}

	called, name, args := up.called()
	if !called {
		t.Fatalf("upstream tools/call was not invoked")
	}
	if name != "commit" {
		t.Fatalf("upstream got name=%q, want the real tool name 'commit'", name)
	}
	var gotArgs map[string]any
	if err := json.Unmarshal(args, &gotArgs); err != nil {
		t.Fatalf("upstream args not JSON: %v", err)
	}
	if gotArgs["message"] != "hello" {
		t.Fatalf("upstream args = %v, want message=hello", gotArgs)
	}
}

// TestMCPCallValidationRejectsBadArgs: mcp_call with a missing required field
// and mcp_call with a wrong-typed field both yield a shaped aggregator error and
// NEVER dispatch upstream.
func TestMCPCallValidationRejectsBadArgs(t *testing.T) {
	t.Run("missing required", func(t *testing.T) {
		up := &callableUpstream{tools: []fakeTool{
			{name: "commit", description: "d", schema: `{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`},
		}}
		h, srv := buildHandler(t, up)
		defer srv.Close()

		result, isErr := callVerb(t, h, "mcp_call", map[string]any{
			"tool_id": "srv.commit",
			"args":    map[string]any{},
		})
		if !isErr && !resultIsError(t, result) {
			t.Fatalf("mcp_call with missing required arg should be a shaped error, got: %s", result)
		}
		if called, _, _ := up.called(); called {
			t.Fatalf("upstream must NOT be called when validation fails")
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		up := &callableUpstream{tools: []fakeTool{
			{name: "commit", description: "d", schema: `{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`},
		}}
		h, srv := buildHandler(t, up)
		defer srv.Close()

		result, isErr := callVerb(t, h, "mcp_call", map[string]any{
			"tool_id": "srv.commit",
			"args":    map[string]any{"message": 42},
		})
		if !isErr && !resultIsError(t, result) {
			t.Fatalf("mcp_call with wrong-typed arg should be a shaped error, got: %s", result)
		}
		if called, _, _ := up.called(); called {
			t.Fatalf("upstream must NOT be called when validation fails")
		}
	})
}

// TestMCPCallUnknownID: mcp_call(unknown id) is a shaped error and never
// dispatches upstream.
func TestMCPCallUnknownID(t *testing.T) {
	up := &callableUpstream{tools: []fakeTool{
		{name: "commit", description: "d"},
	}}
	h, srv := buildHandler(t, up)
	defer srv.Close()

	result, isErr := callVerb(t, h, "mcp_call", map[string]any{
		"tool_id": "srv.ghost",
		"args":    map[string]any{},
	})
	if !isErr && !resultIsError(t, result) {
		t.Fatalf("mcp_call(unknown) should be a shaped error, got: %s", result)
	}
	if called, _, _ := up.called(); called {
		t.Fatalf("upstream must NOT be called for an unknown tool_id")
	}
}

// TestMCPCallToolErrorPassthrough: an upstream tool that RAN and returned an
// isError CallToolResult must pass through VERBATIM — not be re-wrapped as an
// aggregator error.
func TestMCPCallToolErrorPassthrough(t *testing.T) {
	up := &callableUpstream{
		tools: []fakeTool{
			{name: "commit", description: "d", schema: `{"type":"object","properties":{}}`},
		},
		callResult: `{"content":[{"type":"text","text":"nothing to commit"}],"isError":true}`,
	}
	h, srv := buildHandler(t, up)
	defer srv.Close()

	result, isErr := callVerb(t, h, "mcp_call", map[string]any{
		"tool_id": "srv.commit",
		"args":    map[string]any{},
	})
	if isErr {
		t.Fatalf("a tool that ran should return a JSON-RPC result, not an error envelope: %s", result)
	}
	if !resultIsError(t, result) {
		t.Fatalf("the tool's isError result must pass through verbatim (isError:true)")
	}
	if got := resultText(t, result); got != "nothing to commit" {
		t.Fatalf("tool error content not verbatim: got %q", got)
	}
	if called, _, _ := up.called(); !called {
		t.Fatalf("upstream should have been called for a valid tool that returns an error result")
	}
}

// TestMCPCallRetriesOnStaleSession: an upstream that rejects a cached (now
// invalid) session id makes the first dispatch fail; the handler must invalidate
// the cached id, re-initialize for a fresh one, and retry the tools/call ONCE —
// which then succeeds. The tool must ultimately run exactly once (the stale
// attempt is rejected before running), and the upstream must have received two
// tools/call requests (stale + fresh).
func TestMCPCallRetriesOnStaleSession(t *testing.T) {
	up := &callableUpstream{
		sessionAware: true,
		tools: []fakeTool{
			{name: "commit", description: "d", schema: `{"type":"object","properties":{}}`},
		},
		callResult: `{"content":[{"type":"text","text":"done"}]}`,
	}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()
	agg, err := NewAggregator(context.Background(), []Upstream{{Name: "srv", URL: srv.URL}}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}
	h := NewHandler(agg)

	// First call succeeds and caches session id S1.
	result, isErr := callVerb(t, h, "mcp_call", map[string]any{
		"tool_id": "srv.commit", "args": map[string]any{},
	})
	if isErr || resultIsError(t, result) {
		t.Fatalf("first mcp_call should succeed: %s", result)
	}

	// Upstream "restarts": S1 is now stale. The next call's cached id will be
	// rejected, forcing the retry path.
	up.rotateSession()

	result, isErr = callVerb(t, h, "mcp_call", map[string]any{
		"tool_id": "srv.commit", "args": map[string]any{},
	})
	if isErr {
		t.Fatalf("stale-session call should recover, not return a JSON-RPC error: %s", result)
	}
	if resultIsError(t, result) {
		t.Fatalf("stale-session call should recover via retry, got shaped error: %s", resultText(t, result))
	}
	if got := resultText(t, result); got != "done" {
		t.Fatalf("recovered call result = %q, want 'done'", got)
	}
}

// TestMCPCallNoRetryLoopWhenAlwaysStale: if EVERY dispatch is rejected — even
// one carrying a freshly-initialized session id — the handler must retry exactly
// ONCE and then surface a shaped error, never looping. Assert the upstream
// received exactly two tools/call requests (original + one retry) and the shaped
// error is returned.
func TestMCPCallNoRetryLoopWhenAlwaysStale(t *testing.T) {
	up := &callableUpstream{
		sessionAware:     true,
		alwaysRejectCall: true,
		tools: []fakeTool{
			{name: "commit", description: "d", schema: `{"type":"object","properties":{}}`},
		},
	}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()
	agg, err := NewAggregator(context.Background(), []Upstream{{Name: "srv", URL: srv.URL}}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}
	h := NewHandler(agg)

	result, _ := callVerb(t, h, "mcp_call", map[string]any{
		"tool_id": "srv.commit", "args": map[string]any{},
	})
	if !resultIsError(t, result) {
		t.Fatalf("an always-failing dispatch should surface a shaped error, got: %s", result)
	}
	// Exactly two tools/call requests: the original attempt and the single retry.
	// More than two would mean the retry looped.
	received, ran := up.callTally()
	if ran {
		t.Fatalf("the tool must never actually run when every dispatch is rejected")
	}
	if received != 2 {
		t.Fatalf("expected exactly 2 tools/call requests (original + one retry), got %d", received)
	}
}

// TestMCPCallConcurrentSameUpstream: many concurrent mcp_calls to the SAME
// upstream must all succeed with no data race on the session-cache map. Run
// under -race to exercise the lock discipline (the benign double-init race, no
// map corruption). The upstream here is session-continuity-free (empty id) so
// the test isolates the cache-map lock discipline from session-rotation churn —
// the stale-session retry path is covered by its own dedicated tests.
func TestMCPCallConcurrentSameUpstream(t *testing.T) {
	up := &callableUpstream{
		tools: []fakeTool{
			{name: "commit", description: "d", schema: `{"type":"object","properties":{}}`},
		},
		callResult: `{"content":[{"type":"text","text":"ok"}]}`,
	}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()
	agg, err := NewAggregator(context.Background(), []Upstream{{Name: "srv", URL: srv.URL}}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}
	h := NewHandler(agg)

	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args, _ := json.Marshal(map[string]any{"tool_id": "srv.commit", "args": map[string]any{}})
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": fmt.Sprintf("%d", i), "method": "tools/call",
				"params": map[string]any{"name": "mcp_call", "arguments": json.RawMessage(args)},
			})
			resp, err := h.SendRequest(context.Background(), fmt.Sprintf(`"%d"`, i), body)
			if err != nil {
				errs[i] = err
				return
			}
			var env struct {
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(resp, &env); err != nil {
				errs[i] = err
				return
			}
			if resultIsError(t, env.Result) {
				errs[i] = fmt.Errorf("call %d got shaped error: %s", i, env.Result)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent call %d failed: %v", i, err)
		}
	}
	received, ran := up.callTally()
	if !ran {
		t.Fatalf("upstream never ran a tool under concurrent load")
	}
	if received < n {
		t.Fatalf("upstream received %d tools/call requests, want at least %d", received, n)
	}
}

// TestMCPCallJSONEscaping: a tool name and arg values containing characters that
// MUST be JSON-escaped (quotes, backslashes, newlines) must reach the upstream as
// a well-formed tools/call the upstream can parse, carrying the EXACT original
// strings. Regression against the hand-built JSON-RPC dispatch body.
func TestMCPCallJSONEscaping(t *testing.T) {
	const trickyTool = `he said "hi"\and\so`
	up := &callableUpstream{
		tools: []fakeTool{
			{name: trickyTool, description: "d", schema: `{"type":"object","properties":{}}`},
		},
	}
	srv := httptest.NewServer(up.handler())
	defer srv.Close()
	agg, err := NewAggregator(context.Background(), []Upstream{{Name: "srv", URL: srv.URL}}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}
	h := NewHandler(agg)

	toolID := "srv." + trickyTool
	trickyValue := "line1\nline2 \"quoted\" back\\slash"
	result, isErr := callVerb(t, h, "mcp_call", map[string]any{
		"tool_id": toolID,
		"args":    map[string]any{"msg": trickyValue},
	})
	if isErr {
		t.Fatalf("mcp_call with tricky strings returned a JSON-RPC error: %s", result)
	}
	if resultIsError(t, result) {
		t.Fatalf("mcp_call with tricky strings should dispatch cleanly, got shaped error: %s", resultText(t, result))
	}

	// The upstream must have received a WELL-FORMED body it could parse, and the
	// parsed name/args must equal the exact originals (proving correct escaping,
	// not corruption or injection).
	raw := up.lastCallBody()
	if raw == nil {
		t.Fatalf("upstream received no tools/call body")
	}
	var env struct {
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("upstream body was not well-formed JSON: %v\nbody: %s", err, raw)
	}
	if env.Params.Name != trickyTool {
		t.Fatalf("upstream name = %q, want the exact original %q", env.Params.Name, trickyTool)
	}
	var gotArgs map[string]string
	if err := json.Unmarshal(env.Params.Arguments, &gotArgs); err != nil {
		t.Fatalf("upstream arguments not parseable: %v (%s)", err, env.Params.Arguments)
	}
	if gotArgs["msg"] != trickyValue {
		t.Fatalf("upstream arg msg = %q, want the exact original %q", gotArgs["msg"], trickyValue)
	}
	if called, name, _ := up.called(); !called || name != trickyTool {
		t.Fatalf("upstream did not run the tricky-named tool (called=%v name=%q)", called, name)
	}
}

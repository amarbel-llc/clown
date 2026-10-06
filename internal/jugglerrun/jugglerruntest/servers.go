package jugglerruntest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// FakeMCP is an MCP streamable-HTTP upstream standing in for moxy: initialize
// answers with an Mcp-Session-Id, tools/list with tools (the JSON "tools"
// array) and every tools/call with callResult (a CallToolResult object).
func FakeMCP(t testing.TB, tools, callResult string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(MCPHandler(tools, callResult, func(err error) { t.Errorf("bad MCP request: %v", err) }))
	t.Cleanup(srv.Close)
	return srv
}

// MCPHandler is FakeMCP's handler; onBadRequest sees an undecodable request.
func MCPHandler(tools, callResult string, onBadRequest func(error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && onBadRequest != nil {
			onBadRequest(err)
		}
		result := `{}`
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = `{"protocolVersion":"2025-06-18","capabilities":{}}`
		case "tools/list":
			result = `{"tools":` + tools + `}`
		case "tools/call":
			result = callResult
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	})
}

// ScriptedModel is an OpenAI-compatible chat-completions endpoint (serve it
// as URL+"/v1") answering request N with replies[N]; the last reply repeats.
func ScriptedModel(t testing.TB, replies ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(ScriptedModelHandler(replies, func(path string) { t.Errorf("model path = %s", path) }, nil))
	t.Cleanup(srv.Close)
	return srv
}

// ScriptedModelHandler is ScriptedModel's handler. onBadPath sees a request
// to a path other than /v1/chat/completions; onRequest, when set, is called
// with each request's index.
func ScriptedModelHandler(replies []string, onBadPath func(string), onRequest func(int)) http.Handler {
	var mu sync.Mutex
	n := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" && onBadPath != nil {
			onBadPath(r.URL.Path)
		}
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		i := min(n, len(replies)-1)
		n++
		mu.Unlock()
		if onRequest != nil {
			onRequest(i)
		}
		_, _ = io.WriteString(w, replies[i])
	})
}

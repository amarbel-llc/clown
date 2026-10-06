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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad MCP request: %v", err)
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
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ScriptedModel is an OpenAI-compatible chat-completions endpoint (serve it
// as URL+"/v1") answering request N with replies[N]; the last reply repeats.
func ScriptedModel(t testing.TB, replies ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("model path = %s", r.URL.Path)
		}
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		i := min(n, len(replies)-1)
		n++
		mu.Unlock()
		_, _ = io.WriteString(w, replies[i])
	}))
	t.Cleanup(srv.Close)
	return srv
}

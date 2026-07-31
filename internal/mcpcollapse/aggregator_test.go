package mcpcollapse

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/mcphttp"
)

// fakeUpstream is a configurable MCP-over-HTTP server standing in for a real
// upstream. It answers initialize and tools/list; each behavior is a field so a
// test can make a server healthy, fail tools/list, or require a session-id echo.
type fakeUpstream struct {
	// tools is the catalog returned from a healthy tools/list.
	tools []fakeTool
	// failToolsList, when set, makes tools/list return HTTP 500 — the fail-open
	// trigger the aggregator must skip rather than abort on.
	failToolsList bool
	// requireSession, when true, makes initialize hand back a session id and
	// makes tools/list 400 unless that id is echoed on the request, mirroring
	// moxy's streamhttp session-continuity requirement.
	requireSession bool
	// sessionID is the id handed back from initialize when requireSession.
	sessionID string
}

type fakeTool struct {
	name        string
	description string
	schema      string
}

func (f *fakeUpstream) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(bodyBytes, &envelope)

		switch envelope.Method {
		case "initialize":
			if f.requireSession {
				w.Header().Set(mcphttp.MCPSessionIDHeader, f.sessionID)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(envelope.ID) + `,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake","version":"1"}}}`))
		case "tools/list":
			if f.failToolsList {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			if f.requireSession && r.Header.Get(mcphttp.MCPSessionIDHeader) != f.sessionID {
				http.Error(w, "missing Mcp-Session-Id header", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(envelope.ID) + `,"result":{"tools":[` + f.toolsJSON() + `]}}`))
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}
}

func (f *fakeUpstream) toolsJSON() string {
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

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestAggregatorTwoHealthyUpstreams: two healthy upstreams enumerate fully; the
// built registry contains every tool under its dotted {server}.{tool} id, the
// raw inputSchema survives verbatim, and Degraded is empty.
func TestAggregatorTwoHealthyUpstreams(t *testing.T) {
	srvA := httptest.NewServer((&fakeUpstream{tools: []fakeTool{
		{name: "commit", description: "make a commit", schema: `{"type":"object"}`},
		{name: "status", description: "show status"},
	}}).handler())
	defer srvA.Close()
	srvB := httptest.NewServer((&fakeUpstream{tools: []fakeTool{
		{name: "search", description: "search the web"},
	}}).handler())
	defer srvB.Close()

	agg, err := NewAggregator(context.Background(), []Upstream{
		{Name: "grit", URL: srvA.URL},
		{Name: "web", URL: srvB.URL},
	}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}

	if deg := agg.Degraded(); len(deg) != 0 {
		t.Fatalf("expected no degraded upstreams, got %v", deg)
	}

	reg := agg.Registry()
	wantIDs := []string{"grit.commit", "grit.status", "web.search"}
	for _, id := range wantIDs {
		entry, ok := reg.Lookup(id)
		if !ok {
			t.Fatalf("expected registry to contain %q", id)
		}
		if entry.ID() != id {
			t.Fatalf("entry.ID() = %q, want %q", entry.ID(), id)
		}
	}
	if got := len(reg.Entries()); got != len(wantIDs) {
		t.Fatalf("registry has %d entries, want %d", got, len(wantIDs))
	}

	commit, _ := reg.Lookup("grit.commit")
	if strings.TrimSpace(string(commit.Schema)) != `{"type":"object"}` {
		t.Fatalf("commit schema = %q, want {\"type\":\"object\"}", string(commit.Schema))
	}
}

// TestAggregatorFailOpenSkipsBrokenUpstream: one healthy upstream plus one whose
// tools/list 500s. The registry is built from the healthy one only; the broken
// one lands in Degraded with its name, URL, and a non-nil reason. Fail-open.
func TestAggregatorFailOpenSkipsBrokenUpstream(t *testing.T) {
	healthy := httptest.NewServer((&fakeUpstream{tools: []fakeTool{
		{name: "search", description: "search the web"},
	}}).handler())
	defer healthy.Close()
	broken := httptest.NewServer((&fakeUpstream{failToolsList: true}).handler())
	defer broken.Close()

	agg, err := NewAggregator(context.Background(), []Upstream{
		{Name: "web", URL: healthy.URL},
		{Name: "broken", URL: broken.URL},
	}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}

	reg := agg.Registry()
	if _, ok := reg.Lookup("web.search"); !ok {
		t.Fatalf("expected healthy upstream's tool web.search in registry")
	}
	if got := len(reg.Entries()); got != 1 {
		t.Fatalf("registry has %d entries, want 1 (only the healthy upstream)", got)
	}

	deg := agg.Degraded()
	if len(deg) != 1 {
		t.Fatalf("expected 1 degraded upstream, got %d: %v", len(deg), deg)
	}
	if deg[0].Name != "broken" {
		t.Fatalf("degraded name = %q, want %q", deg[0].Name, "broken")
	}
	if deg[0].URL != broken.URL {
		t.Fatalf("degraded URL = %q, want %q", deg[0].URL, broken.URL)
	}
	if deg[0].Err == nil {
		t.Fatalf("expected a non-nil reason on the degraded upstream")
	}
}

// TestAggregatorEchoesSessionID: an upstream that hands back an Mcp-Session-Id on
// initialize and 400s tools/list without it. Successful enumeration proves the
// aggregator captured the id from initialize and echoed it on tools/list.
func TestAggregatorEchoesSessionID(t *testing.T) {
	up := httptest.NewServer((&fakeUpstream{
		requireSession: true,
		sessionID:      "sess-123",
		tools: []fakeTool{
			{name: "ping", description: "ping"},
		},
	}).handler())
	defer up.Close()

	agg, err := NewAggregator(context.Background(), []Upstream{
		{Name: "gated", URL: up.URL},
	}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}
	if deg := agg.Degraded(); len(deg) != 0 {
		t.Fatalf("session-gated upstream should enumerate; degraded=%v", deg)
	}
	if _, ok := agg.Registry().Lookup("gated.ping"); !ok {
		t.Fatalf("expected gated.ping in registry (session id must have been echoed)")
	}
}

// TestAggregatorHealthGatePopulatedOnReturn: the constructor returning IS the
// health gate. Both the registry and the degraded list must be complete the
// instant NewAggregator returns, with no fan-out still in flight.
func TestAggregatorHealthGatePopulatedOnReturn(t *testing.T) {
	up := httptest.NewServer((&fakeUpstream{tools: []fakeTool{
		{name: "a", description: "a"},
	}}).handler())
	defer up.Close()
	broken := httptest.NewServer((&fakeUpstream{failToolsList: true}).handler())
	defer broken.Close()

	agg, err := NewAggregator(context.Background(), []Upstream{
		{Name: "ok", URL: up.URL},
		{Name: "bad", URL: broken.URL},
	}, time.Second)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}

	if agg.Registry() == nil {
		t.Fatalf("Registry() nil immediately after constructor returned")
	}
	if got := len(agg.Registry().Entries()); got != 1 {
		t.Fatalf("registry not fully populated on return: %d entries, want 1", got)
	}
	if got := len(agg.Degraded()); got != 1 {
		t.Fatalf("degraded not fully populated on return: %d, want 1", got)
	}
}

// TestAggregatorDuplicateServerNameFails: two upstreams sharing a name is a
// config error, not a fail-open case — the registry Build rejects it and
// construction fails outright.
func TestAggregatorDuplicateServerNameFails(t *testing.T) {
	srvA := httptest.NewServer((&fakeUpstream{tools: []fakeTool{
		{name: "one", description: "one"},
	}}).handler())
	defer srvA.Close()
	srvB := httptest.NewServer((&fakeUpstream{tools: []fakeTool{
		{name: "two", description: "two"},
	}}).handler())
	defer srvB.Close()

	_, err := NewAggregator(context.Background(), []Upstream{
		{Name: "dup", URL: srvA.URL},
		{Name: "dup", URL: srvB.URL},
	}, time.Second)
	if err == nil {
		t.Fatalf("expected construction to fail on duplicate server name")
	}
	if !strings.Contains(err.Error(), "duplicate server name") {
		t.Fatalf("error should mention duplicate server name, got: %v", err)
	}
}

package mcphttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// clientMaxBytes is a generous bound for the client tests; the responses
// under test never approach it, so it never affects the parsed result.
const clientMaxBytes = 1024 * 1024

func TestExtractSSEData(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "single data event",
			raw:  "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n",
			want: `{"jsonrpc":"2.0","id":1,"result":{}}`,
		},
		{
			name: "heartbeats then final event: last one wins",
			raw:  ": heartbeat 0\n\ndata: {\"a\":1}\n\ndata: {\"a\":2}\n\n",
			want: `{"a":2}`,
		},
		{
			name:    "no data event",
			raw:     ": heartbeat 0\n\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractSSEData([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("ExtractSSEData() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPostJSONRPC_SSEResponse verifies PostJSONRPC correctly extracts a
// JSON-RPC response framed as text/event-stream — the default mode
// clown-stdio-bridge answers POSTs in (heartbeatMode streams unless
// CLOWN_BRIDGE_HEARTBEAT_INTERVAL=0), which callers must handle since clown
// doesn't control that env var per-fetch.
func TestPostJSONRPC_SSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A heartbeat/progress event precedes the final response event; only
		// the last data: payload is the JSON-RPC message.
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":\"x\",\"result\":{\"tools\":[]}}\n\n"))
	}))
	defer srv.Close()

	body, _, err := PostJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`, clientMaxBytes)
	if err != nil {
		t.Fatalf("PostJSONRPC: %v", err)
	}
	want := `{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`
	if string(body) != want {
		t.Errorf("PostJSONRPC() = %q, want %q", body, want)
	}
}

// TestPostJSONRPC_PlainJSONResponse verifies the non-streaming path still
// works (a server that answers with application/json directly).
func TestPostJSONRPC_PlainJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`))
	}))
	defer srv.Close()

	body, _, err := PostJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`, clientMaxBytes)
	if err != nil {
		t.Fatalf("PostJSONRPC: %v", err)
	}
	want := `{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`
	if string(body) != want {
		t.Errorf("PostJSONRPC() = %q, want %q", body, want)
	}
}

// TestPostJSONRPC_SessionIDRoundTrip verifies PostJSONRPC captures a
// response's Mcp-Session-Id header and echoes a provided sessionID back on
// the request — the continuity moxy's native httpServers implementation
// requires between initialize and tools/list (internal/streamhttp,
// amarbel-llc/moxy): a follow-up call without the header 400s even though
// initialize itself succeeded.
func TestPostJSONRPC_SessionIDRoundTrip(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(MCPSessionIDHeader)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(MCPSessionIDHeader, "sess-123")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"x","result":{}}`))
	}))
	defer srv.Close()

	_, sessionID, err := PostJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"initialize"}`, clientMaxBytes)
	if err != nil {
		t.Fatalf("PostJSONRPC (initialize): %v", err)
	}
	if sessionID != "sess-123" {
		t.Fatalf("captured sessionID = %q, want sess-123", sessionID)
	}
	if gotHeader != "" {
		t.Errorf("initialize request unexpectedly sent %s=%q", MCPSessionIDHeader, gotHeader)
	}

	if _, _, err := PostJSONRPC(context.Background(), srv.URL, sessionID, `{"jsonrpc":"2.0","id":"y","method":"tools/list"}`, clientMaxBytes); err != nil {
		t.Fatalf("PostJSONRPC (tools/list): %v", err)
	}
	if gotHeader != "sess-123" {
		t.Errorf("tools/list request sent %s=%q, want sess-123", MCPSessionIDHeader, gotHeader)
	}
}

// TestPostJSONRPC_ZeroMaxBytesUsesDefault verifies a non-positive maxBytes
// falls back to DefaultMaxResponseBytes and reads the full body, rather than
// io.LimitReader(_, 0)'s silent zero-byte read that would return an empty
// "success" — the failure mode the aggregator's many call sites could trip.
func TestPostJSONRPC_ZeroMaxBytesUsesDefault(t *testing.T) {
	const wantBody = `{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(wantBody))
	}))
	defer srv.Close()

	body, _, err := PostJSONRPC(context.Background(), srv.URL, "", `{}`, 0)
	if err != nil {
		t.Fatalf("PostJSONRPC: %v", err)
	}
	if string(body) != wantBody {
		t.Errorf("PostJSONRPC(..., 0) = %q, want %q (should use DefaultMaxResponseBytes, not truncate to empty)", body, wantBody)
	}
}

// TestPostJSONRPC_Non200 verifies a non-200 status is surfaced as an error
// rather than a body — FetchToolCatalog relies on this to degrade to
// (nil, false) instead of parsing an error page as JSON-RPC.
func TestPostJSONRPC_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, _, err := PostJSONRPC(context.Background(), srv.URL, "", `{}`, clientMaxBytes); err == nil {
		t.Fatal("PostJSONRPC: want error for non-200 status, got nil")
	}
}

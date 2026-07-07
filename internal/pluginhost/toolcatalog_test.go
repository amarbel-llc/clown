package pluginhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
			got, err := extractSSEData([]byte(tc.raw))
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
				t.Errorf("extractSSEData() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPostJSONRPC_SSEResponse verifies postJSONRPC correctly extracts a
// JSON-RPC response framed as text/event-stream — the default mode
// clown-stdio-bridge answers POSTs in (heartbeatMode streams unless
// CLOWN_BRIDGE_HEARTBEAT_INTERVAL=0), which FetchToolCatalog must handle
// since clown doesn't control that env var per-fetch.
func TestPostJSONRPC_SSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":\"x\",\"result\":{\"tools\":[]}}\n\n"))
	}))
	defer srv.Close()

	h := &Host{}
	body, _, err := h.postJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`)
	if err != nil {
		t.Fatalf("postJSONRPC: %v", err)
	}
	want := `{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`
	if string(body) != want {
		t.Errorf("postJSONRPC() = %q, want %q", body, want)
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

	h := &Host{}
	body, _, err := h.postJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`)
	if err != nil {
		t.Fatalf("postJSONRPC: %v", err)
	}
	want := `{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`
	if string(body) != want {
		t.Errorf("postJSONRPC() = %q, want %q", body, want)
	}
}

// TestPostJSONRPC_SessionIDRoundTrip verifies postJSONRPC captures a
// response's Mcp-Session-Id header and echoes a provided sessionID back on
// the request — the continuity moxy's native httpServers implementation
// requires between initialize and tools/list (internal/streamhttp,
// amarbel-llc/moxy): a follow-up call without the header 400s even though
// initialize itself succeeded.
func TestPostJSONRPC_SessionIDRoundTrip(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(mcpSessionIDHeader)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(mcpSessionIDHeader, "sess-123")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"x","result":{}}`))
	}))
	defer srv.Close()

	h := &Host{}
	_, sessionID, err := h.postJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"initialize"}`)
	if err != nil {
		t.Fatalf("postJSONRPC (initialize): %v", err)
	}
	if sessionID != "sess-123" {
		t.Fatalf("captured sessionID = %q, want sess-123", sessionID)
	}
	if gotHeader != "" {
		t.Errorf("initialize request unexpectedly sent %s=%q", mcpSessionIDHeader, gotHeader)
	}

	if _, _, err := h.postJSONRPC(context.Background(), srv.URL, sessionID, `{"jsonrpc":"2.0","id":"y","method":"tools/list"}`); err != nil {
		t.Fatalf("postJSONRPC (tools/list): %v", err)
	}
	if gotHeader != "sess-123" {
		t.Errorf("tools/list request sent %s=%q, want sess-123", mcpSessionIDHeader, gotHeader)
	}
}

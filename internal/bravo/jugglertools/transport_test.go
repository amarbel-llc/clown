package jugglertools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const transportTestMaxBytes = 1 << 20

func TestExtractSSEData(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "single data event", raw: "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n", want: `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{name: "heartbeats then final event: last one wins", raw: ": heartbeat 0\n\ndata: {\"a\":1}\n\ndata: {\"a\":2}\n\n", want: `{"a":2}`},
		{name: "no space after colon", raw: "data:{\"a\":3}\n\n", want: `{"a":3}`},
		{name: "no data event", raw: ": heartbeat 0\n\n", wantErr: true},
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

func TestPostJSONRPC_SSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":\"x\",\"result\":{\"tools\":[]}}\n\n"))
	}))
	defer srv.Close()

	body, _, err := postJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`, transportTestMaxBytes)
	if err != nil {
		t.Fatalf("postJSONRPC: %v", err)
	}
	if want := `{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`; string(body) != want {
		t.Errorf("postJSONRPC() = %q, want %q", body, want)
	}
}

func TestPostJSONRPC_PlainJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`))
	}))
	defer srv.Close()

	body, _, err := postJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`, transportTestMaxBytes)
	if err != nil {
		t.Fatalf("postJSONRPC: %v", err)
	}
	if want := `{"jsonrpc":"2.0","id":"x","result":{"tools":[]}}`; string(body) != want {
		t.Errorf("postJSONRPC() = %q, want %q", body, want)
	}
}

// moxy requires the session id from initialize on every later call.
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

	_, sessionID, err := postJSONRPC(context.Background(), srv.URL, "", `{"jsonrpc":"2.0","id":"x","method":"initialize"}`, transportTestMaxBytes)
	if err != nil {
		t.Fatalf("postJSONRPC (initialize): %v", err)
	}
	if sessionID != "sess-123" {
		t.Fatalf("captured sessionID = %q, want sess-123", sessionID)
	}
	if gotHeader != "" {
		t.Errorf("initialize request unexpectedly sent %s=%q", mcpSessionIDHeader, gotHeader)
	}

	if _, _, err := postJSONRPC(context.Background(), srv.URL, sessionID, `{"jsonrpc":"2.0","id":"y","method":"tools/list"}`, transportTestMaxBytes); err != nil {
		t.Fatalf("postJSONRPC (tools/list): %v", err)
	}
	if gotHeader != "sess-123" {
		t.Errorf("tools/list request sent %s=%q, want sess-123", mcpSessionIDHeader, gotHeader)
	}
}

func TestPostJSONRPC_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, _, err := postJSONRPC(context.Background(), srv.URL, "", `{}`, transportTestMaxBytes); err == nil {
		t.Fatal("postJSONRPC: want error for non-200 status, got nil")
	}
}

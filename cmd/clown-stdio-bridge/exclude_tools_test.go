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
)

func TestFilterToolsListResponse(t *testing.T) {
	h := &httpHandler{}

	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"folio_read","description":"a"},{"name":"folio_glob","description":"b"},{"name":"grit_status","description":"c"}]}}`)

	// No exclusions: body passes through unmodified.
	if got := h.filterToolsListResponse(body); !bytes.Equal(got, body) {
		t.Errorf("with no exclusions, body was modified: got %s", got)
	}

	h.setExcludeTools([]string{"folio_read", "folio_glob"})
	got := h.filterToolsListResponse(body)

	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("filtered body is not valid JSON: %v\nbody: %s", err, got)
	}
	if len(parsed.Result.Tools) != 1 || parsed.Result.Tools[0].Name != "grit_status" {
		t.Errorf("filtered tools = %+v, want only grit_status", parsed.Result.Tools)
	}

	// Excluding everything yields an empty (not missing) tools array.
	h.setExcludeTools([]string{"folio_read", "folio_glob", "grit_status"})
	got = h.filterToolsListResponse(body)
	parsed.Result.Tools = nil
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("filtered body is not valid JSON: %v\nbody: %s", err, got)
	}
	if len(parsed.Result.Tools) != 0 {
		t.Errorf("filtered tools = %+v, want empty", parsed.Result.Tools)
	}

	// Clearing the exclude set (empty slice) restores pass-through.
	h.setExcludeTools(nil)
	if got := h.filterToolsListResponse(body); !bytes.Equal(got, body) {
		t.Errorf("after clearing exclusions, body was modified: got %s", got)
	}
}

func TestFilterToolsListResponse_NonToolsListBodyPassesThrough(t *testing.T) {
	h := &httpHandler{}
	h.setExcludeTools([]string{"anything"})

	// A tools/call response (or any non-tools/list shape) has no
	// result.tools array; filtering must be a no-op, not an error.
	body := []byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}]}}`)
	if got := h.filterToolsListResponse(body); !bytes.Equal(got, body) {
		t.Errorf("non-tools/list body was modified: got %s", got)
	}
}

// TestHTTP_ExcludeToolsEndpointFiltersToolsList is an end-to-end check: POST
// /clown/exclude-tools sets the exclude list on a running handler, then a
// tools/list request through handleMCP returns the filtered catalog. Uses
// the runTranslator fake-child harness from http_test.go, extended here
// with a child that answers tools/list with a real MCP-shaped result.
func TestHTTP_ExcludeToolsEndpointFiltersToolsList(t *testing.T) {
	// Synchronous JSON path (heartbeats off), simplest to assert on. The env
	// var name is the spine's public contract (mcphttp owns the const now).
	t.Setenv("CLOWN_BRIDGE_HEARTBEAT_INTERVAL", "0")

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
				if msg["method"] != "tools/list" {
					continue
				}
				resp := map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]any{
						"tools": []map[string]any{
							{"name": "folio_read"},
							{"name": "grit_status"},
						},
					},
				}
				out, _ := json.Marshal(resp)
				_, _ = stdoutW.Write(append(out, '\n'))
			}
		}
	}()

	h := newHTTPHandler(tr, nullLogger{}, nil)
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
	defer srv.Close()

	h.setExcludeTools([]string{"folio_read"})

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	got, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, got)
	}
	if len(parsed.Result.Tools) != 1 || parsed.Result.Tools[0].Name != "grit_status" {
		t.Errorf("tools/list response = %+v, want only grit_status", parsed.Result.Tools)
	}
}

// TestHandleExcludeTools_Contract verifies GET/POST /clown/exclude-tools
// against the contract clown-stdio-bridge now shares with moxy's
// independently-shipped endpoint (amarbel-llc/moxy#399): body key
// "exclude", 200 status, both methods echo back the resulting set.
func TestHandleExcludeTools_Contract(t *testing.T) {
	h := &httpHandler{logger: nullLogger{}}
	srv := httptest.NewServer(http.HandlerFunc(h.handleExcludeTools))
	defer srv.Close()

	// GET with nothing excluded yet.
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var body struct {
		Exclude []string `json:"exclude"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET status = %d, want 200", resp.StatusCode)
	}
	if len(body.Exclude) != 0 {
		t.Errorf("GET exclude = %v, want empty", body.Exclude)
	}

	// POST replaces the set and echoes it back.
	reqBody := `{"exclude":["folio","grit_status"]}`
	resp, err = http.Post(srv.URL, "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST status = %d, want 200", resp.StatusCode)
	}
	body.Exclude = nil
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	resp.Body.Close()
	got := map[string]bool{}
	for _, n := range body.Exclude {
		got[n] = true
	}
	if !got["folio"] || !got["grit_status"] || len(got) != 2 {
		t.Errorf("POST echoed exclude = %v, want [folio grit_status]", body.Exclude)
	}

	// GET now reflects the POSTed set.
	resp, err = http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET after POST: %v", err)
	}
	body.Exclude = nil
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode second GET response: %v", err)
	}
	resp.Body.Close()
	if len(body.Exclude) != 2 {
		t.Errorf("GET after POST exclude = %v, want 2 entries", body.Exclude)
	}
}

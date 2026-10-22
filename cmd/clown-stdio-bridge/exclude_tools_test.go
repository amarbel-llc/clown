package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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

// TestHTTP_ExcludedToolCallIsRefused: hiding a tool from tools/list is not
// enough, so a tools/call naming an excluded tool is answered by the bridge
// with moxy's tool-error shape and never reaches the child, while a call to
// a kept tool still does.
func TestHTTP_ExcludedToolCallIsRefused(t *testing.T) {
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

	var mu sync.Mutex
	var called []string
	go func() {
		dec := json.NewDecoder(stdinR)
		for {
			var msg struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if err := dec.Decode(&msg); err != nil {
				return
			}
			if msg.Method != "tools/call" {
				continue
			}
			mu.Lock()
			called = append(called, msg.Params.Name)
			mu.Unlock()
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"content": []any{}}})
			_, _ = stdoutW.Write(append(out, '\n'))
		}
	}()

	h := newHTTPHandler(tr, nullLogger{}, nil)
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
	defer srv.Close()
	h.setExcludeTools([]string{"folio_read"})

	call := func(name string) map[string]any {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		var parsed map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}
		return parsed
	}

	refused := call("folio_read")
	result, _ := refused["result"].(map[string]any)
	if result["isError"] != true || refused["id"] != float64(7) {
		t.Errorf("excluded call answered %v, want an isError result for id 7", refused)
	}
	if kept := call("grit_status"); kept["result"] == nil {
		t.Errorf("kept call answered %v, want the child's result", kept)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(called, []string{"grit_status"}) {
		t.Errorf("child saw calls %v, want only grit_status", called)
	}
}

// TestHTTP_MultiLineBodyReachesTheChildAsOneMessage: a child that reads one
// message per line must not see a tools/call smuggled as an inner line of a
// harmless request (audit 2026-10-22, top 4).
func TestHTTP_MultiLineBodyReachesTheChildAsOneMessage(t *testing.T) {
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

	var mu sync.Mutex
	var lines []string
	go func() {
		sc := bufio.NewScanner(stdinR)
		for sc.Scan() {
			mu.Lock()
			lines = append(lines, sc.Text())
			mu.Unlock()
			var msg struct {
				ID any `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil {
				continue
			}
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
			_, _ = stdoutW.Write(append(out, '\n'))
		}
	}()

	h := newHTTPHandler(tr, nullLogger{}, nil)
	srv := httptest.NewServer(http.HandlerFunc(h.handleMCP))
	defer srv.Close()
	h.setExcludeTools([]string{"folio_read"})

	body := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\",\"x\":\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"folio_read"}}` + "\n}"
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], `{"jsonrpc":"2.0","id":1,"method":"ping"`) {
		t.Errorf("child read lines %q, want the ping as one line", lines)
	}
}

func TestRefuseExcludedCall(t *testing.T) {
	h := &httpHandler{}
	h.setExcludeTools([]string{"folio_read"})
	cases := []struct {
		name, body         string
		refused, hasAnswer bool
	}{
		{"excluded call", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"folio_read"}}`, true, true},
		{"kept call", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"grit_status"}}`, false, false},
		{"case-variant key cannot hide the name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"folio_read","NAME":"grit_status"}}`, true, true},
		{"a Go child reads Name over name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"grit_status","Name":"folio_read"}}`, true, true},
		{"case-variant key names an excluded tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"NAME":"folio_read"}}`, true, true},
		{"case-variant method", `{"jsonrpc":"2.0","id":1,"Method":"tools/call","params":{"name":"folio_read"}}`, true, true},
		{"method smuggled behind a duplicate", `{"jsonrpc":"2.0","id":1,"method":"ping","METHOD":"tools/call","params":{"name":"folio_read"}}`, true, true},
		{"exact duplicate name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"grit_status","name":"folio_read"}}`, true, true},
		{"long-s fold of params", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"paramſ\":{\"name\":\"folio_read\"}}", true, true},
		{"escaped name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"folio_read"}}`, true, true},
		{"id-less call is dropped, not forwarded", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"folio_read"}}`, true, false},
		{"other method", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, refused := h.refuseExcludedCall([]byte(c.body))
			if refused != c.refused || (resp != nil) != c.hasAnswer {
				t.Errorf("refused=%v answer=%s, want refused=%v answer=%v", refused, resp, c.refused, c.hasAnswer)
			}
		})
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

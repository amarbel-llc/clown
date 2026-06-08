package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// mcpCall drives serveJobMCP with a single JSON-RPC request and returns the
// parsed response. The server is stateless (all state lives in jobwake's files
// under XDG_STATE_HOME), so one call per request lets a test thread the job id
// from job_start into later calls.
func mcpCall(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var out bytes.Buffer
	serveJobMCP(bytes.NewReader(append(b, '\n')), &out)
	line := strings.TrimSpace(out.String())
	if line == "" {
		return nil // notification: no response
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	return resp
}

func toolCall(name string, args map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}
}

// toolCallText extracts the first text content block and the isError flag from a
// tools/call response.
func toolCallText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result: %v", resp)
	}
	isErr, _ := result["isError"].(bool)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return "", isErr
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text, isErr
}

func TestJobMCPInitializeAndToolsList(t *testing.T) {
	resp := mcpCall(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: no result: %v", resp)
	}
	si, _ := result["serverInfo"].(map[string]any)
	if si["name"] != "clown-jobs" {
		t.Fatalf("serverInfo.name = %v, want clown-jobs", si["name"])
	}

	resp = mcpCall(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	result, _ = resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{
		"job_start", "job_progress", "job_done", "job_message",
		"job_read", "job_status", "job_spool_path",
	} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}
	if len(tools) != 7 {
		t.Errorf("want 7 tools, got %d", len(tools))
	}
}

func TestJobMCPToolCallRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	// job_done sends a (best-effort) nudge; a short XDG_RUNTIME_DIR keeps the
	// socket path under the AF_UNIX sun_path limit even though the dial is
	// error-tolerant.
	rt, err := os.MkdirTemp("/tmp", "clown-mcp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(rt)
	t.Setenv("XDG_RUNTIME_DIR", rt)

	resp := mcpCall(t, toolCall("job_start", map[string]any{"source": "moxy", "label": "build"}))
	id, isErr := toolCallText(t, resp)
	if isErr || id == "" {
		t.Fatalf("job_start: id=%q isErr=%v", id, isErr)
	}

	resp = mcpCall(t, toolCall("job_status", map[string]any{"job_id": id}))
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("job_status running errored: %s", text)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		t.Fatalf("status json %q: %v", text, err)
	}
	if st["state"] != "running" || st["source"] != "moxy" {
		t.Fatalf("status = %s, want running/moxy", text)
	}

	resp = mcpCall(t, toolCall("job_done", map[string]any{"job_id": id, "state": "succeeded", "message": "ok"}))
	if _, isErr := toolCallText(t, resp); isErr {
		t.Fatal("job_done errored")
	}

	resp = mcpCall(t, toolCall("job_status", map[string]any{"job_id": id}))
	text, _ = toolCallText(t, resp)
	_ = json.Unmarshal([]byte(text), &st)
	if st["state"] != "succeeded" {
		t.Fatalf("status after done = %s, want succeeded", text)
	}

	resp = mcpCall(t, toolCall("job_spool_path", map[string]any{"job_id": id}))
	path, isErr := toolCallText(t, resp)
	if isErr || !strings.HasSuffix(path, id+".out") {
		t.Fatalf("job_spool_path = %q isErr=%v", path, isErr)
	}
}

func TestJobMCPJobReadReturnsRecords(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	resp := mcpCall(t, toolCall("job_start", map[string]any{"source": "s"}))
	id, _ := toolCallText(t, resp)

	resp = mcpCall(t, toolCall("job_read", map[string]any{"job": id}))
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("job_read errored: %s", text)
	}
	var recs []map[string]any
	if err := json.Unmarshal([]byte(text), &recs); err != nil {
		t.Fatalf("job_read json %q: %v", text, err)
	}
	if len(recs) != 1 || recs[0]["type"] != "started" {
		t.Fatalf("job_read records = %s, want one started", text)
	}
}

func TestJobMCPInvalidJobIDIsToolError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "k")
	resp := mcpCall(t, toolCall("job_status", map[string]any{"job_id": "../passwd"}))
	if _, isErr := toolCallText(t, resp); !isErr {
		t.Fatal("job_status with traversal id must return isError")
	}
}

func TestJobMCPUnknownMethodErrors(t *testing.T) {
	resp := mcpCall(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "frobnicate"})
	if resp["error"] == nil {
		t.Fatalf("unknown method must return a JSON-RPC error, got %v", resp)
	}
}

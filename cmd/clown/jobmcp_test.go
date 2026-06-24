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
// from job_start into later calls. An optional surface (clown#144) selects the
// tool slice; omitted means the whole catalog.
func mcpCall(t *testing.T, req map[string]any, surface ...string) map[string]any {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	s := ""
	if len(surface) > 0 {
		s = surface[0]
	}
	var out bytes.Buffer
	serveJobMCP(bytes.NewReader(append(b, '\n')), &out, s)
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
	// The prompts capability MUST be advertised so claude (and the bridge's
	// prompts/get fetch) know the server offers prompts (RFC-0002 §5).
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["prompts"]; !ok {
		t.Errorf("initialize capabilities missing prompts: %v", caps)
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
		"job_read", "job_status", "job_spool_path", "job_wait",
		"chat_send", "chat_read", "chat_list",
	} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}
	if len(tools) != 11 {
		t.Errorf("want 11 tools, got %d", len(tools))
	}
}

// listToolNames drives tools/list on a surface and returns the advertised tool
// names plus the serverInfo.name from initialize.
func listToolNames(t *testing.T, surface string) (map[string]bool, string) {
	t.Helper()
	initResp := mcpCall(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}, surface)
	si, _ := initResp["result"].(map[string]any)["serverInfo"].(map[string]any)
	resp := mcpCall(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, surface)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	return names, si["name"].(string)
}

// clown#144: the ringmaster surface exposes only the job-control tools (and
// reports the clown-ringmaster server identity), none of the messaging tools.
func TestJobMCPRingmasterSurface(t *testing.T) {
	names, server := listToolNames(t, "ringmaster")
	if server != "clown-ringmaster" {
		t.Errorf("serverInfo.name = %q, want clown-ringmaster", server)
	}
	for _, want := range []string{"job_start", "job_progress", "job_done", "job_read", "job_status", "job_spool_path", "job_wait"} {
		if !names[want] {
			t.Errorf("ringmaster surface missing %q", want)
		}
	}
	for _, unwanted := range []string{"job_message", "chat_send", "chat_read", "chat_list"} {
		if names[unwanted] {
			t.Errorf("ringmaster surface should not expose messaging tool %q", unwanted)
		}
	}
	if len(names) != 7 {
		t.Errorf("ringmaster surface tool count = %d, want 7", len(names))
	}
}

// clown#144: the troupe surface exposes only the messaging tools (chat + the
// standalone job_message) and reports the clown-troupe server identity.
func TestJobMCPTroupeSurface(t *testing.T) {
	names, server := listToolNames(t, "troupe")
	if server != "clown-troupe" {
		t.Errorf("serverInfo.name = %q, want clown-troupe", server)
	}
	for _, want := range []string{"job_message", "chat_send", "chat_read", "chat_list"} {
		if !names[want] {
			t.Errorf("troupe surface missing %q", want)
		}
	}
	for _, unwanted := range []string{"job_start", "job_done", "job_status", "job_wait"} {
		if names[unwanted] {
			t.Errorf("troupe surface should not expose job-control tool %q", unwanted)
		}
	}
	if len(names) != 4 {
		t.Errorf("troupe surface tool count = %d, want 4", len(names))
	}
}

func TestJobMCPPromptsList(t *testing.T) {
	resp := mcpCall(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "prompts/list"})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("prompts/list: no result: %v", resp)
	}
	prompts, _ := result["prompts"].([]any)
	if len(prompts) != 1 {
		t.Fatalf("want 1 prompt, got %d (%v)", len(prompts), prompts)
	}
	first, _ := prompts[0].(map[string]any)
	if first["name"] != jobSystemPromptName {
		t.Errorf("prompt name = %v, want %q", first["name"], jobSystemPromptName)
	}
}

func TestJobMCPPromptsGet(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "session-xyz")
	resp := mcpCall(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompts/get",
		"params": map[string]any{"name": jobSystemPromptName},
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("prompts/get: no result: %v", resp)
	}
	messages, _ := result["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("prompts/get returned no messages: %v", result)
	}
	content, _ := messages[0].(map[string]any)["content"].(map[string]any)
	text, _ := content["text"].(string)
	// Runtime-dynamic, server-owned: the injected session key and the server's
	// own tool catalog both appear — neither is expressible at build time.
	if !strings.Contains(text, "session-xyz") {
		t.Errorf("fragment missing injected session key; got:\n%s", text)
	}
	if !strings.Contains(text, "job_start") {
		t.Errorf("fragment missing tool catalog; got:\n%s", text)
	}
	if !strings.Contains(text, "clown job platform") {
		t.Errorf("fragment missing header; got:\n%s", text)
	}
}

func TestJobMCPPromptsGetUnknown(t *testing.T) {
	resp := mcpCall(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompts/get",
		"params": map[string]any{"name": "no-such-prompt"},
	})
	if _, ok := resp["error"]; !ok {
		t.Errorf("prompts/get for unknown name should error, got %v", resp)
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

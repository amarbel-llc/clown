package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

func TestMCPServe_Initialize(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil)

	var resp struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if resp.Result.ServerInfo.Name != "juggler" {
		t.Errorf("serverInfo.name = %q, want juggler", resp.Result.ServerInfo.Name)
	}
}

func TestMCPServe_ToolsList(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil)

	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if len(resp.Result.Tools) != 1 || resp.Result.Tools[0].Name != "juggler-prompt" {
		t.Fatalf("tools = %+v, want exactly one juggler-prompt tool", resp.Result.Tools)
	}
	req := resp.Result.Tools[0].InputSchema.Required
	if len(req) != 2 || req[0] != "model" || req[1] != "task" {
		t.Errorf("required params = %v, want [model task]", req)
	}
}

// TestMCPServe_ToolsCall_HappyPath drives a full tools/call round trip:
// fake juggler daemon resolves "my-model" to a fake remote HTTP endpoint,
// Serve dispatches the call, and the MCP tool-result content carries the
// model's reply.
func TestMCPServe_ToolsCall_HappyPath(t *testing.T) {
	var gotPrompt string
	remoteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		_ = json.Unmarshal(body, &req)
		if len(req.Messages) == 1 {
			gotPrompt = req.Messages[0].Content
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"delegated reply"}]}`))
	}))
	defer remoteSrv.Close()

	fakeResolveModelDaemon(t, rm.ResolveModelResult{
		Kind: rm.ModelKindRemote, URL: remoteSrv.URL, Token: "sekret", Style: "anthropic",
	})
	cli, err := dialClient()
	if err != nil {
		t.Fatalf("dialClient: %v", err)
	}
	defer cli.Close()

	params := `{"name":"juggler-prompt","arguments":{"model":"my-model","task":"do the thing"}}`
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, cli)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if resp.Result.IsError {
		t.Fatalf("unexpected isError, content: %+v", resp.Result.Content)
	}
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Text != "delegated reply" {
		t.Errorf("content = %+v, want [{delegated reply}]", resp.Result.Content)
	}
	if gotPrompt != "do the thing" {
		t.Errorf("prompt sent to model = %q, want %q", gotPrompt, "do the thing")
	}
}

// TestMCPServe_ToolsCall_DaemonUnreachable verifies the "daemon
// unreachable at dial time" case surfaces as an isError tool result
// (agent-readable), not a JSON-RPC transport error or a panic —
// exercised via a nil cli, which is what callJugglerPromptTool sees when
// dialClient() never succeeded. (Previously misnamed
// TestMCPServe_ToolsCall_UnknownModel: it never drove a real
// model-not-found response from a live daemon — see
// TestMCPServe_ToolsCall_ModelNotFound for that case.)
func TestMCPServe_ToolsCall_DaemonUnreachable(t *testing.T) {
	params := `{"name":"juggler-prompt","arguments":{"model":"nope","task":"x"}}`
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil) // nil cli: the "daemon unreachable" case this test wants

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if !resp.Result.IsError {
		t.Fatal("want isError=true for an unreachable daemon")
	}
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Text == "" {
		t.Errorf("content = %+v, want a non-empty error message", resp.Result.Content)
	}
}

// fakeResolveModelErrorDaemon starts a fake juggler control-socket
// listener that answers exactly one ResolveModel-shaped RPC call with a
// JSON-RPC error envelope (code/message), mirroring the "alias not
// found" fixture already used by TestCmdStatus_AliasNotFound in
// status_test.go. Sets JUGGLER_SOCKET via t.Setenv so dialClient() finds
// it.
func fakeResolveModelErrorDaemon(t *testing.T, code int, message string) {
	t.Helper()
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := rm.ReadFrame(br)
		if err != nil {
			return
		}
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rm.Error{Code: code, Message: message},
		})
	}()
	t.Setenv("JUGGLER_SOCKET", socket)
}

// TestMCPServe_ToolsCall_ModelNotFound drives ResolveModel to a real
// not-found error via a LIVE client talking to a fake daemon (the design
// doc's Testing section: "Unknown model name -> toolErr surfacing the
// -32001 not-found case"), distinguishing this from the nil-client
// daemon-unreachable path covered by
// TestMCPServe_ToolsCall_DaemonUnreachable.
func TestMCPServe_ToolsCall_ModelNotFound(t *testing.T) {
	fakeResolveModelErrorDaemon(t, -32001, `model "nope" not found (local or remote)`)
	cli, err := dialClient()
	if err != nil {
		t.Fatalf("dialClient: %v", err)
	}
	defer cli.Close()

	params := `{"name":"juggler-prompt","arguments":{"model":"nope","task":"x"}}`
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, cli)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if !resp.Result.IsError {
		t.Fatal("want isError=true for a not-found model")
	}
	if len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, "not found") {
		t.Errorf("content = %+v, want an error mentioning \"not found\"", resp.Result.Content)
	}
}

// TestMCPServe_ToolsCall_RemoteSendFails exercises a resolved model whose
// endpoint then rejects the completion request itself (a non-2xx from
// the remote server) — sendPrompt's error must surface as isError
// content, not a panic or bare transport failure.
func TestMCPServe_ToolsCall_RemoteSendFails(t *testing.T) {
	remoteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer remoteSrv.Close()

	fakeResolveModelDaemon(t, rm.ResolveModelResult{
		Kind: rm.ModelKindRemote, URL: remoteSrv.URL, Token: "sekret", Style: "anthropic",
	})
	cli, err := dialClient()
	if err != nil {
		t.Fatalf("dialClient: %v", err)
	}
	defer cli.Close()

	params := `{"name":"juggler-prompt","arguments":{"model":"my-model","task":"do the thing"}}`
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, cli)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if !resp.Result.IsError {
		t.Fatal("want isError=true when the remote endpoint rejects the request")
	}
	if len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, "500") {
		t.Errorf("content = %+v, want an error mentioning the 500 status", resp.Result.Content)
	}
}

// TestMCPServe_ToolsCall_MalformedParams verifies a tools/call whose
// params, while valid JSON at the envelope level, don't unmarshal into
// callJugglerPromptTool's expected {name, arguments} shape (here: params
// is a bare JSON string, not an object) surfaces as isError content
// rather than a panic. This is distinct from an unparseable envelope
// line (which Serve skips outright, per the "skip unparseable line"
// comment) — this test exercises callJugglerPromptTool's own
// json.Unmarshal error path.
func TestMCPServe_ToolsCall_MalformedParams(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"not-an-object"}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if !resp.Result.IsError {
		t.Fatal("want isError=true for malformed tools/call params")
	}
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Text == "" {
		t.Errorf("content = %+v, want a non-empty error message", resp.Result.Content)
	}
}

// TestMCPServe_ToolsCall_UnknownToolName verifies a tools/call naming a
// tool other than juggler-prompt is rejected with isError content
// instead of being silently dispatched to the wrong handler.
func TestMCPServe_ToolsCall_UnknownToolName(t *testing.T) {
	params := `{"name":"not-a-real-tool","arguments":{"model":"m","task":"t"}}`
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}` + "\n")
	var out bytes.Buffer
	Serve(in, &out, nil)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.Bytes())
	}
	if !resp.Result.IsError {
		t.Fatal("want isError=true for an unrecognized tool name")
	}
	if len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, "not-a-real-tool") {
		t.Errorf("content = %+v, want an error mentioning the unknown tool name", resp.Result.Content)
	}
}

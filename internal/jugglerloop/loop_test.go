package jugglerloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// scriptedModel is an httptest model endpoint that answers request N with
// replies[N] (the last reply repeats) and records every request body.
type scriptedModel struct {
	t       *testing.T
	path    string
	replies []string

	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
}

func newScriptedModel(t *testing.T, path string, replies ...string) (*scriptedModel, *httptest.Server) {
	m := &scriptedModel{t: t, path: path, replies: replies}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return m, srv
}

func (m *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != m.path {
		m.t.Errorf("path = %s, want %s", r.URL.Path, m.path)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		m.t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		m.t.Errorf("request body is not JSON: %v", err)
	}
	m.mu.Lock()
	n := len(m.requests)
	m.requests = append(m.requests, decoded)
	m.headers = append(m.headers, r.Header.Clone())
	m.mu.Unlock()
	if n >= len(m.replies) {
		n = len(m.replies) - 1
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(m.replies[n]))
}

func (m *scriptedModel) request(i int) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i >= len(m.requests) {
		m.t.Fatalf("only %d requests recorded, wanted index %d", len(m.requests), i)
	}
	return m.requests[i]
}

// fakeExecutor answers each tool name with a canned outcome and records
// the calls it saw.
type fakeExecutor struct {
	outcomes map[string]fakeOutcome
	calls    []string
}

type fakeOutcome struct {
	content string
	isError bool
	err     error
}

func (f *fakeExecutor) Call(_ context.Context, name string, _ json.RawMessage) (json.RawMessage, bool, error) {
	f.calls = append(f.calls, name)
	o, ok := f.outcomes[name]
	if !ok {
		return json.RawMessage(`"unknown tool"`), true, nil
	}
	if o.err != nil {
		return nil, false, o.err
	}
	return json.RawMessage(o.content), o.isError, nil
}

func sequentialIDs() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("t%d", n)
	}
}

var issueTools = []ToolSpec{
	{Name: "list_repos", Description: "List repos", InputSchema: json.RawMessage(`{"type":"object"}`)},
	{Name: "create_issue", Description: "File an issue", InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`), Kind: "issue"},
}

func baseConfig(resolved rm.ResolveModelResult, exec ToolExecutor) Config {
	return Config{
		Resolved:     resolved,
		Model:        "test-model",
		SystemPrompt: "you file issues",
		Task:         "file one issue",
		Tools:        issueTools,
		Exec:         exec,
		Principal:    "agent-1",
		BriefTurnID:  "brief-0",
		NewTurnID:    sequentialIDs(),
	}
}

const (
	anthropicCreateIssueReply = `{"content":[{"type":"text","text":"filing"},{"type":"tool_use","id":"tu_1","name":"create_issue","input":{"title":"x"}}],"stop_reason":"tool_use"}`
	anthropicEndTurnReply     = `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`
	openAICreateIssueReply    = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"create_issue","arguments":"{\"title\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`
	openAIEndTurnReply        = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`
	openAICannotCompleteReply = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_9","type":"function","function":{"name":"cannot_complete","arguments":"{\"reason\":\"no repo matches\"}"}}]},"finish_reason":"tool_calls"}]}`
)

func createIssueExecutor() *fakeExecutor {
	return &fakeExecutor{outcomes: map[string]fakeOutcome{
		"create_issue": {content: `{"url":"https://code.example/clown/issues/301"}`},
		"list_repos":   {content: `["clown"]`},
	}}
}

func TestRun_Anthropic_ToolCallThenEndTurn(t *testing.T) {
	model, srv := newScriptedModel(t, "/v1/messages", anthropicCreateIssueReply, anthropicEndTurnReply)
	exec := createIssueExecutor()
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, exec)
	var seen []Turn
	cfg.OnTurn = func(turn Turn) { seen = append(seen, turn) }

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.End != EndTurn || res.Ledger.End.Reason != EndTurn {
		t.Errorf("end = %q / %q, want end_turn", res.End, res.Ledger.End.Reason)
	}
	if res.Ledger.Steps != 2 {
		t.Errorf("steps = %d, want 2", res.Ledger.Steps)
	}
	if len(res.Ledger.Calls) != 1 {
		t.Fatalf("calls = %+v, want 1", res.Ledger.Calls)
	}
	call := res.Ledger.Calls[0]
	if call.Tool != "create_issue" || call.Kind == nil || *call.Kind != "issue" || !call.OK {
		t.Errorf("call = %+v", call)
	}
	if len(call.URIs) != 1 || call.URIs[0] != "https://code.example/clown/issues/301" {
		t.Errorf("uris = %v", call.URIs)
	}
	if res.Ledger.CannotComplete != nil {
		t.Errorf("cannot_complete = %+v, want nil", res.Ledger.CannotComplete)
	}

	// text, tool_call, tool_result, text, terminal
	wantTypes := []BodyType{BodyText, BodyToolCall, BodyToolResult, BodyText, BodyTerminal}
	if len(res.Transcript.Turns) != len(wantTypes) {
		t.Fatalf("turns = %+v", res.Transcript.Turns)
	}
	for i, want := range wantTypes {
		got := res.Transcript.Turns[i]
		if got.Body.Type != want {
			t.Errorf("turn %d type = %s, want %s", i, got.Body.Type, want)
		}
		if got.Sender != "agent-1" {
			t.Errorf("turn %d sender = %q", i, got.Sender)
		}
	}
	if res.Transcript.Schema != 1 {
		t.Errorf("schema = %d", res.Transcript.Schema)
	}
	if p := res.Transcript.Turns[0].Parent; p != "brief-0" {
		t.Errorf("first turn parent = %q, want brief-0", p)
	}
	if p := res.Transcript.Turns[2].Parent; p != res.Transcript.Turns[1].ID {
		t.Errorf("tool_result parent = %q, want the tool_call turn %q", p, res.Transcript.Turns[1].ID)
	}
	if len(seen) != len(res.Transcript.Turns) {
		t.Errorf("OnTurn saw %d turns, transcript has %d", len(seen), len(res.Transcript.Turns))
	}

	if got := model.headers[0].Get("x-api-key"); got != "dummy" {
		t.Errorf("x-api-key = %q, want dummy for a local model", got)
	}
	if got := model.headers[0].Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	first := model.request(0)
	if first["system"] != "you file issues" {
		t.Errorf("system = %v", first["system"])
	}
	tools := first["tools"].([]any)
	if len(tools) != 3 || tools[2].(map[string]any)["name"] != CannotCompleteTool {
		t.Errorf("tools = %v, want 2 + cannot_complete", tools)
	}
	second := model.request(1)
	msgs := second["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second request messages = %v", msgs)
	}
	toolResult := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "tu_1" {
		t.Errorf("tool_result block = %v", toolResult)
	}
}

func TestRun_OpenAICompat_ToolCallThenEndTurn(t *testing.T) {
	model, srv := newScriptedModel(t, "/v1/chat/completions", openAICreateIssueReply, openAIEndTurnReply)
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL + "/v1", Token: "sk-test", Style: "openai-compat"}, createIssueExecutor())

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.End != EndTurn || res.Ledger.Steps != 2 || len(res.Ledger.Calls) != 1 || !res.Ledger.Calls[0].OK {
		t.Errorf("result = %+v", res.Ledger)
	}
	if got := model.headers[0].Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	msgs := model.request(1)["messages"].([]any)
	// system, user task, assistant tool_calls, tool
	if len(msgs) != 4 {
		t.Fatalf("messages = %v", msgs)
	}
	if msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "file one issue" {
		t.Errorf("opening messages = %v", msgs[:2])
	}
	assistant := msgs[2].(map[string]any)
	fn := assistant["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "create_issue" || fn["arguments"] != `{"title":"x"}` {
		t.Errorf("assistant tool call = %v", fn)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != `{"url":"https://code.example/clown/issues/301"}` {
		t.Errorf("tool message = %v", tool)
	}
}

func TestRun_CannotComplete(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/chat/completions", openAICannotCompleteReply)
	exec := createIssueExecutor()
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL + "/v1", Style: "openai-compat"}, exec)

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.End != EndCannotComplete {
		t.Errorf("end = %q", res.End)
	}
	if res.Ledger.CannotComplete == nil || res.Ledger.CannotComplete.Reason != "no repo matches" {
		t.Errorf("cannot_complete = %+v", res.Ledger.CannotComplete)
	}
	if len(res.Ledger.Calls) != 0 || len(exec.calls) != 0 {
		t.Errorf("cannot_complete must not reach the executor or the calls list: %v %v", res.Ledger.Calls, exec.calls)
	}
	last := res.Transcript.Turns[len(res.Transcript.Turns)-1]
	if last.Body.Type != BodyTerminal || last.Body.Terminal.Reason != EndCannotComplete {
		t.Errorf("last turn = %+v", last)
	}
}

func TestRun_StepCap(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/messages", anthropicCreateIssueReply)
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, createIssueExecutor())
	cfg.MaxSteps = 2

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.End != EndStepCap || res.Ledger.Steps != 2 || len(res.Ledger.Calls) != 2 {
		t.Errorf("ledger = %+v", res.Ledger)
	}
}

func TestRun_WallClock(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	// Cleanups run LIFO: unblock the handler before srv.Close waits on it.
	t.Cleanup(func() { close(release) })
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, createIssueExecutor())
	cfg.WallClock = 50 * time.Millisecond

	start := time.Now()
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v (the brief's own wall clock is not an error)", err)
	}
	if res.End != EndTimeout {
		t.Errorf("end = %q, want timeout", res.End)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("run took %s, wall clock not enforced", elapsed)
	}
}

func TestRun_ParentCancelReturnsCtxError(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/messages", anthropicEndTurnReply)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, createIssueExecutor())

	res, err := Run(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if res.End != EndTimeout {
		t.Errorf("end = %q, want timeout", res.End)
	}
}

func TestRun_ToolTransportErrorEndsRun(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/messages", anthropicCreateIssueReply, anthropicEndTurnReply)
	exec := &fakeExecutor{outcomes: map[string]fakeOutcome{"create_issue": {err: errors.New("moxy unreachable")}}}
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, exec)

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.End != EndToolError || res.Ledger.Steps != 1 {
		t.Errorf("ledger = %+v", res.Ledger)
	}
	if len(res.Ledger.Calls) != 1 || res.Ledger.Calls[0].OK || res.Ledger.Calls[0].Error != "moxy unreachable" {
		t.Errorf("calls = %+v", res.Ledger.Calls)
	}
}

func TestRun_ToolLevelErrorIsSeenByModel(t *testing.T) {
	model, srv := newScriptedModel(t, "/v1/messages", anthropicCreateIssueReply, anthropicEndTurnReply)
	exec := &fakeExecutor{outcomes: map[string]fakeOutcome{"create_issue": {content: `"permission denied"`, isError: true}}}
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, exec)

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.End != EndTurn {
		t.Errorf("end = %q, want end_turn (an isError result is not a run-ending tool error)", res.End)
	}
	c := res.Ledger.Calls[0]
	if c.OK || c.Error != "permission denied" || len(c.URIs) != 0 {
		t.Errorf("call = %+v", c)
	}
	block := model.request(1)["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["is_error"] != true || block["content"] != "permission denied" {
		t.Errorf("tool_result block = %v", block)
	}
}

func TestRun_ModelHTTPErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, createIssueExecutor())

	res, err := Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run: want error for a 502 from the model")
	}
	if res.End != EndModelError {
		t.Errorf("end = %q, want model_error", res.End)
	}
}

func TestRun_RejectsUnknownStyle(t *testing.T) {
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindRemote, Style: "decisions"}, createIssueExecutor())
	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("want error for a style with no loop codec")
	}
}

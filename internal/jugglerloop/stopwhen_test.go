package jugglerloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

const anthropicListReposReply = `{"content":[{"type":"tool_use","id":"tu_0","name":"list_repos","input":{}}],"stop_reason":"tool_use"}`

// stopWhenRun runs a two-tool-call script (list_repos, then create_issue,
// then end_turn) with stopWhen.
func stopWhenRun(t *testing.T, stopWhen func(json.RawMessage) (bool, error)) (*scriptedModel, Result) {
	t.Helper()
	model, srv := newScriptedModel(t, "/v1/messages", anthropicListReposReply, anthropicCreateIssueReply, anthropicEndTurnReply)
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, createIssueExecutor())
	cfg.StopWhen = stopWhen
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return model, res
}

func TestRun_StopWhenPassesAfterTheFirstCall(t *testing.T) {
	var seen []Ledger
	model, res := stopWhenRun(t, func(doc json.RawMessage) (bool, error) {
		var l Ledger
		if err := json.Unmarshal(doc, &l); err != nil {
			t.Fatalf("in-progress ledger: %v", err)
		}
		seen = append(seen, l)
		return len(l.Calls) >= 1 && l.Calls[0].OK, nil
	})
	if res.End != EndEvaluatorPass || res.Ledger.End.Reason != EndEvaluatorPass {
		t.Fatalf("end = %q / %q", res.End, res.Ledger.End.Reason)
	}
	if n := len(model.requests); n != 1 || res.Ledger.Steps != 1 {
		t.Errorf("model requests = %d, steps = %d, want 1", n, res.Ledger.Steps)
	}
	if len(seen) != 1 || seen[0].End.Reason != "" || seen[0].Schema != LedgerSchema || len(seen[0].Calls) != 1 {
		t.Errorf("StopWhen saw %+v", seen)
	}
	if len(res.Ledger.Calls) != 1 || len(res.Ledger.Notes) != 0 {
		t.Errorf("ledger = %+v", res.Ledger)
	}
	if last := res.Transcript.Turns[len(res.Transcript.Turns)-1]; last.Body.Type != BodyTerminal || last.Body.Terminal.Reason != EndEvaluatorPass {
		t.Errorf("terminal turn = %+v", last)
	}
}

func TestRun_WithoutStopWhenRunsToEndTurn(t *testing.T) {
	model, res := stopWhenRun(t, nil)
	if res.End != EndTurn || len(model.requests) != 3 || len(res.Ledger.Calls) != 2 {
		t.Errorf("end = %q, requests = %d, calls = %d", res.End, len(model.requests), len(res.Ledger.Calls))
	}
}

func TestRun_StopWhenSkipsFailedCalls(t *testing.T) {
	// A tool-level error (isError, also how jugglertools reports JSON-RPC
	// -32602), then arguments the loop rejects itself, then a success.
	denied := `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"list_repos","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	badArgs := `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c2","type":"function","function":{"name":"create_issue","arguments":"{\"title\":\"x"}}]},"finish_reason":"tool_calls"}]}`
	_, srv := newScriptedModel(t, "/v1/chat/completions", denied, badArgs, openAICreateIssueReply, openAIEndTurnReply)
	exec := &fakeExecutor{outcomes: map[string]fakeOutcome{
		"list_repos":   {content: `"permission denied"`, isError: true},
		"create_issue": {content: `{"url":"https://code.example/clown/issues/301"}`},
	}}
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL + "/v1", Style: "openai-compat"}, exec)
	var seen []Ledger
	cfg.StopWhen = func(doc json.RawMessage) (bool, error) {
		var l Ledger
		_ = json.Unmarshal(doc, &l)
		seen = append(seen, l)
		return true, nil
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || len(seen[0].Calls) != 3 || seen[0].Calls[0].OK || seen[0].Calls[1].OK || !seen[0].Calls[2].OK {
		t.Fatalf("StopWhen must run only after the successful third call: %+v", seen)
	}
	if res.End != EndEvaluatorPass || res.Ledger.Steps != 3 {
		t.Errorf("end = %q, steps = %d", res.End, res.Ledger.Steps)
	}
}

func TestRun_StopWhenSeesTheCallsURIsAndKind(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/messages", anthropicCreateIssueReply, anthropicEndTurnReply)
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, createIssueExecutor())
	var seen []Ledger
	cfg.StopWhen = func(doc json.RawMessage) (bool, error) {
		var l Ledger
		_ = json.Unmarshal(doc, &l)
		seen = append(seen, l)
		return false, nil
	}
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || len(seen[0].Calls) != 1 {
		t.Fatalf("seen = %+v", seen)
	}
	c := seen[0].Calls[0]
	if c.Kind == nil || *c.Kind != "issue" || len(c.URIs) != 1 || c.URIs[0] != "https://code.example/clown/issues/301" {
		t.Errorf("the in-progress call must be complete: %+v", c)
	}
}

func TestRun_StopWhenMidReplyLeavesTheRestUnanswered(t *testing.T) {
	both := `{"content":[{"type":"tool_use","id":"tu_a","name":"list_repos","input":{}},{"type":"tool_use","id":"tu_b","name":"create_issue","input":{"title":"x"}}],"stop_reason":"tool_use"}`
	model, srv := newScriptedModel(t, "/v1/messages", both, anthropicEndTurnReply)
	exec := createIssueExecutor()
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, exec)
	cfg.StopWhen = func(json.RawMessage) (bool, error) { return true, nil }
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.End != EndEvaluatorPass || len(model.requests) != 1 {
		t.Fatalf("end = %q, requests = %d", res.End, len(model.requests))
	}
	if strings.Join(exec.calls, ",") != "list_repos" || len(res.Ledger.Calls) != 1 || res.Ledger.Calls[0].Tool != "list_repos" {
		t.Errorf("executed = %v, ledger = %+v", exec.calls, res.Ledger.Calls)
	}
	turns := res.Transcript.Turns
	want := []BodyType{BodyToolCall, BodyToolCall, BodyToolResult, BodyTerminal}
	if len(turns) != len(want) {
		t.Fatalf("turns = %+v", turns)
	}
	for i, w := range want {
		if turns[i].Body.Type != w {
			t.Errorf("turn %d = %s, want %s", i, turns[i].Body.Type, w)
		}
	}
	if turns[1].Body.ToolCall.Name != "create_issue" || turns[2].Parent != turns[0].ID {
		t.Errorf("the second call is requested and unanswered; the one result answers the first: %+v", turns)
	}
}

func TestRun_StopWhenErrorIsANoteAndTheRunGoesOn(t *testing.T) {
	model, res := stopWhenRun(t, func(json.RawMessage) (bool, error) { return false, errors.New("jq: boom") })
	if res.End != EndTurn || len(model.requests) != 3 {
		t.Fatalf("end = %q, requests = %d", res.End, len(model.requests))
	}
	if len(res.Ledger.Notes) != 2 || !strings.Contains(res.Ledger.Notes[0], "jq: boom") {
		t.Errorf("notes = %q", res.Ledger.Notes)
	}
}

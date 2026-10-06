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

func TestRun_StopWhenErrorIsANoteAndTheRunGoesOn(t *testing.T) {
	model, res := stopWhenRun(t, func(json.RawMessage) (bool, error) { return false, errors.New("jq: boom") })
	if res.End != EndTurn || len(model.requests) != 3 {
		t.Fatalf("end = %q, requests = %d", res.End, len(model.requests))
	}
	if len(res.Ledger.Notes) != 2 || !strings.Contains(res.Ledger.Notes[0], "jq: boom") {
		t.Errorf("notes = %q", res.Ledger.Notes)
	}
}

package jugglerloop

import (
	"context"
	"errors"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

// TestRun_OnTrafficOneRecordPerToolCall covers clown#255's core ask: one
// traffic record per tool call, at the same point its LedgerCall is
// appended, with a matching args_digest and the result's size and ok flag —
// across all three of execute's outcomes (normal, tool transport error,
// invalid arguments). cannot_complete gets neither a LedgerCall nor a
// traffic record (it ends the run before either is appended).
func TestRun_OnTrafficOneRecordPerToolCall(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/messages", anthropicCreateIssueReply, anthropicEndTurnReply)
	exec := createIssueExecutor()
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, exec)
	var traffic []TrafficRecord
	cfg.OnTraffic = func(rec TrafficRecord) { traffic = append(traffic, rec) }

	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Ledger.Calls) != 1 || len(traffic) != 1 {
		t.Fatalf("calls = %+v, traffic = %+v, want exactly 1 of each", res.Ledger.Calls, traffic)
	}
	call, rec := res.Ledger.Calls[0], traffic[0]
	if rec.Tool != call.Tool || rec.ArgsDigest != call.ArgsDigest {
		t.Errorf("traffic record = %+v does not match ledger call = %+v", rec, call)
	}
	if rec.Step != 1 {
		t.Errorf("step = %d, want 1", rec.Step)
	}
	if string(rec.Args) != `{"title":"x"}` {
		t.Errorf("args = %s, want the call's full arguments as sent", rec.Args)
	}
	const wantResult = `{"url":"https://code.example/clown/issues/301"}`
	if rec.ResultSize != len(wantResult) {
		t.Errorf("result_size = %d, want %d (len of %s)", rec.ResultSize, len(wantResult), wantResult)
	}
	if !rec.OK {
		t.Error("ok = false, want true")
	}
	if rec.TS.IsZero() {
		t.Error("ts is zero, want the tool_result turn's timestamp")
	}
}

func TestRun_OnTrafficRecordsATransportErrorCallWithZeroResultSize(t *testing.T) {
	_, srv := newScriptedModel(t, "/v1/messages", anthropicCreateIssueReply, anthropicEndTurnReply)
	exec := &fakeExecutor{outcomes: map[string]fakeOutcome{"create_issue": {err: errors.New("moxy unreachable")}}}
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}, exec)
	var traffic []TrafficRecord
	cfg.OnTraffic = func(rec TrafficRecord) { traffic = append(traffic, rec) }

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(traffic) != 1 {
		t.Fatalf("traffic = %+v, want exactly 1 record", traffic)
	}
	if rec := traffic[0]; rec.OK || rec.ResultSize != 0 {
		t.Errorf("traffic record = %+v, want ok=false and result_size=0 (no result content exists)", rec)
	}
}

func TestRun_OnTrafficRecordsAnInvalidArgsCallWithTheArgsAsSent(t *testing.T) {
	truncated := `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"create_issue","arguments":"{\"title\":\"x"}}]},"finish_reason":"length"}]}`
	_, srv := newScriptedModel(t, "/v1/chat/completions", truncated, openAIEndTurnReply)
	exec := createIssueExecutor()
	cfg := baseConfig(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL + "/v1", Style: "openai-compat"}, exec)
	var traffic []TrafficRecord
	cfg.OnTraffic = func(rec TrafficRecord) { traffic = append(traffic, rec) }

	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(traffic) != 1 {
		t.Fatalf("traffic = %+v, want exactly 1 record (the invalid call never reaches the executor, but still gets one)", traffic)
	}
	rec := traffic[0]
	if rec.OK {
		t.Error("ok = true, want false")
	}
	if string(rec.Args) != `"{\"title\":\"x"` {
		t.Errorf("args = %s, want the invalid arguments exactly as sent, not the synthesized error", rec.Args)
	}
}

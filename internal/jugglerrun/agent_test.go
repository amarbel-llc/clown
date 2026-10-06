package jugglerrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglerloop"
)

func TestRunAgent_EndToEndSucceeded(t *testing.T) {
	h := newHarness(t)
	model := scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)
	moxy := fakeMoxy(t)

	out, err := RunAgent(context.Background(), h.agentDeps(openAIResolved(model)), AgentRequest{
		Brief:        parsedBrief(t, "agent-1"),
		BriefStanza:  "brief-0",
		MoxyURL:      moxy.URL,
		EnvPrincipal: "agent-1",
	})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if out.State != StateSucceeded || out.ExitCode() != 0 {
		t.Fatalf("outcome = %+v", out)
	}

	recs := h.fakes.Records(t, testRoot, out.Job)
	if recs[0].Type != "started" || !strings.HasPrefix(recs[1].Message, holderRecordPrefix) {
		t.Errorf("journal head = %+v", recs[:2])
	}
	if got := HoldersFromRecords(toJobRecords(recs)); len(got) != 1 || got[0].Principal != testRoot || got[0].Status != HandleAccepted {
		t.Errorf("holders = %+v", got)
	}
	term := lastRecord(t, recs)
	if term.Type != StateSucceeded || term.ResultRef != h.fakes.SpoolPath(testRoot, out.Job) || countTerminals(recs) != 1 {
		t.Errorf("terminal = %+v", term)
	}
	var progressTurns int
	for _, r := range recs {
		if strings.HasPrefix(r.Message, "turn ") {
			progressTurns++
		}
	}

	var ledger jugglerloop.Ledger
	data, err := os.ReadFile(term.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("spool is not the ledger: %v", err)
	}
	if len(ledger.Calls) != 1 || ledger.Calls[0].Tool != "ring_create_issue" || ledger.Calls[0].Kind == nil || *ledger.Calls[0].Kind != "issue" ||
		len(ledger.Calls[0].URIs) != 1 || ledger.Calls[0].URIs[0] != "https://code.example/clown/issues/301" {
		t.Errorf("ledger calls = %+v", ledger.Calls)
	}

	posts := h.fakes.MUC(t)
	// tool_call, tool_result, text, terminal
	if len(posts) != 4 || progressTurns != len(posts) {
		t.Fatalf("posts = %d, turn progress records = %d", len(posts), progressTurns)
	}
	for i, p := range posts {
		var turn jugglerloop.Turn
		if err := json.Unmarshal([]byte(p.Subject), &turn); err != nil {
			t.Fatalf("post %d subject is not a turn: %v", i, err)
		}
		if p.Room != "run@rooms.test" || p.Body != "" || p.Source != RunSource || turn.Sender != "agent-1" {
			t.Errorf("post %d = %+v / %+v", i, p, turn)
		}
		if i == 0 && turn.Parent != "brief-0" {
			t.Errorf("first turn parent = %q, want the brief stanza", turn.Parent)
		}
	}
	if wakes := h.fakes.Wakes(t); len(wakes) != 0 {
		t.Errorf("juggler run must never emit the exit wake: %+v", wakes)
	}
}

func TestRunAgent_CannotCompleteFails(t *testing.T) {
	h := newHarness(t)
	model := scriptedOpenAI(t, openAICannotComplete)
	out, err := RunAgent(context.Background(), h.agentDeps(openAIResolved(model)), AgentRequest{
		Brief: parsedBrief(t, "agent-1"), MoxyURL: fakeMoxy(t).URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateFailed || out.ExitCode() != ExitFailed || !strings.Contains(out.Message, `cannot_complete: "no repo matches"`) {
		t.Fatalf("outcome = %+v", out)
	}
	term := lastRecord(t, h.fakes.Records(t, testRoot, out.Job))
	if term.Type != StateFailed || term.Message != out.Message {
		t.Errorf("terminal = %+v", term)
	}
	if out.Ledger.CannotComplete == nil || out.Ledger.CannotComplete.Reason != "no repo matches" {
		t.Errorf("ledger = %+v", out.Ledger)
	}
}

func TestRunAgent_EvaluatorFalseFails(t *testing.T) {
	h := newHarness(t)
	model := scriptedOpenAI(t, openAIEndTurn)
	out, err := RunAgent(context.Background(), h.agentDeps(openAIResolved(model)), AgentRequest{
		Brief: parsedBrief(t, "agent-1"), MoxyURL: fakeMoxy(t).URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateFailed || out.Message != "evaluator false" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunAgent_UsesPreStartedJob(t *testing.T) {
	h := newHarness(t)
	job, err := h.deps.Ringmaster.Start(context.Background(), testRoot, AgentJobLabel, SpawnSource)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RunAgent(context.Background(), h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn))), AgentRequest{
		Brief: parsedBrief(t, "agent-1"), Job: job, MoxyURL: fakeMoxy(t).URL,
	})
	if err != nil || out.Job != job || out.State != StateSucceeded {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	if jobs := h.fakes.Jobs(t, testRoot); len(jobs) != 1 {
		t.Errorf("a pre-started job must be adopted, not duplicated: %v", jobs)
	}
}

func blockingModel(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func TestRunAgent_ParentCancelAborts(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	out, err := RunAgent(ctx, h.agentDeps(openAIResolved(blockingModel(t))), AgentRequest{
		Brief: parsedBrief(t, "agent-1"), MoxyURL: fakeMoxy(t).URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateAborted || out.ExitCode() != ExitAborted {
		t.Fatalf("outcome = %+v", out)
	}
	if term := lastRecord(t, h.fakes.Records(t, testRoot, out.Job)); term.Type != StateAborted {
		t.Errorf("terminal = %+v", term)
	}
}

func TestRunAgent_HolderJobCancelAborts(t *testing.T) {
	h := newHarness(t)
	job, err := h.deps.Ringmaster.Start(context.Background(), testRoot, AgentJobLabel, SpawnSource)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(200*time.Millisecond, func() { h.fakes.AppendRecord(t, testRoot, job, "cancel-requested", "holder cancel", "") })
	out, err := RunAgent(context.Background(), h.agentDeps(openAIResolved(blockingModel(t))), AgentRequest{
		Brief: parsedBrief(t, "agent-1"), Job: job, MoxyURL: fakeMoxy(t).URL, WatchCancel: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateAborted {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunAgent_PrincipalMismatchIsConfigError(t *testing.T) {
	h := newHarness(t)
	_, err := RunAgent(context.Background(), h.agentDeps(rm.ResolveModelResult{}), AgentRequest{
		Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "someone-else",
	})
	if err == nil || !strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("err = %v", err)
	}
	if jobs := h.fakes.Jobs(t, testRoot); len(jobs) != 0 {
		t.Errorf("no job may be started on a config error: %v", jobs)
	}
}

func TestRunAgent_LaunchesMoxyFromTheMoxyfile(t *testing.T) {
	h := newHarness(t)
	bin, log := fakeMoxyBin(t, fakeMoxy(t))
	t.Setenv(SessionIDEnv, "agent-1")
	deps := h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn)))
	deps.Moxy = ExecMoxy{Bin: bin}
	deps.AgentStateDir = t.TempDir() + "/agents/agent-1"

	out, err := RunAgent(context.Background(), deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), EnvPrincipal: "agent-1"})
	if err != nil || out.State != StateSucceeded {
		t.Fatalf("outcome = %+v, err = %v", out, err)
	}
	written, err := os.ReadFile(deps.AgentStateDir + "/moxyfile")
	if err != nil || string(written) != "# narrowed to ring" {
		t.Errorf("agent moxyfile = %q (%v)", written, err)
	}
	logged, _ := os.ReadFile(log)
	want := []string{"cwd=" + deps.AgentStateDir, "arg=serve-http", "arg=--name-template", "arg={server}_{tool}", "sid=\n", "moxyfile=yes"}
	if !containsAll(string(logged), want...) {
		t.Errorf("moxy launch log = %q, want %q (principal stripped)", logged, want)
	}
}

func TestRunAgent_UnadvertisedAllowlistedToolIsAConfigError(t *testing.T) {
	h := newHarness(t)
	b := parsedBrief(t, "agent-1")
	b.Tools = append(b.Tools, "ring_delete_everything")
	out, err := RunAgent(context.Background(), h.agentDeps(openAIResolved(scriptedOpenAI(t, openAIEndTurn))), AgentRequest{Brief: b, MoxyURL: fakeMoxy(t).URL})
	if err != nil {
		t.Fatal(err)
	}
	if !out.ConfigError || out.ExitCode() != ExitUsage || !strings.Contains(out.Message, "ring_delete_everything") {
		t.Fatalf("outcome = %+v", out)
	}
	if term := lastRecord(t, h.fakes.Records(t, testRoot, out.Job)); term.Type != StateFailed {
		t.Errorf("the job must still be terminalized: %+v", term)
	}
}

func TestFilterToolsToAllowlist(t *testing.T) {
	specs := []jugglerloop.ToolSpec{{Name: "ring_create_issue"}, {Name: "ring_list_repos"}}
	kept, missing := FilterToolsToAllowlist(specs, []string{"ring_create_issue", "ring_typo"})
	if len(kept) != 1 || kept[0].Name != "ring_create_issue" || len(missing) != 1 || missing[0] != "ring_typo" {
		t.Errorf("kept = %+v, missing = %v", kept, missing)
	}
}

func TestRunAgent_IsErrorResultIsNotOK(t *testing.T) {
	h := newHarness(t)
	moxy := fakeMoxyWith(t, `{"content":[{"type":"text","text":"{\"url\":\"https://x/1\"}"}],"isError":true}`)
	out, err := RunAgent(context.Background(), h.agentDeps(openAIResolved(scriptedOpenAI(t, openAICreateIssue, openAIEndTurn))), AgentRequest{
		Brief: parsedBrief(t, "agent-1"), MoxyURL: moxy.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := out.Ledger.Calls
	if len(c) != 1 || c[0].OK || len(c[0].URIs) != 0 || out.State != StateFailed {
		t.Fatalf("an isError result must be ok=false: %+v / %+v", c, out)
	}
}

func TestRunAgent_SIGTERMPastTheDeadlineIsFailed(t *testing.T) {
	h := newHarness(t)
	var late atomic.Bool
	t0 := time.Now()
	deps := h.agentDeps(openAIResolved(blockingModel(t)))
	deps.Now = func() time.Time {
		if late.Load() {
			return t0.Add(time.Hour)
		}
		return t0
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, func() { late.Store(true); cancel() })
	out, err := RunAgent(ctx, deps, AgentRequest{Brief: parsedBrief(t, "agent-1"), MoxyURL: fakeMoxy(t).URL})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateFailed || out.Message != "wall clock expired" || out.ExitCode() != ExitFailed {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAgentVerdict(t *testing.T) {
	cc := jugglerloop.Result{End: jugglerloop.EndCannotComplete, Ledger: jugglerloop.Ledger{CannotComplete: &jugglerloop.CannotComplete{Reason: "r"}}}
	cases := []struct {
		name      string
		res       jugglerloop.Result
		cancelled bool
		passed    bool
		state     string
		prefix    string
	}{
		{"end_turn pass", jugglerloop.Result{End: jugglerloop.EndTurn}, false, true, StateSucceeded, "evaluator passed"},
		{"end_turn fail", jugglerloop.Result{End: jugglerloop.EndTurn}, false, false, StateFailed, "evaluator false"},
		{"cannot_complete", cc, false, true, StateFailed, `cannot_complete: "r"`},
		{"step cap", jugglerloop.Result{End: jugglerloop.EndStepCap}, false, true, StateFailed, "step_cap:"},
		{"timeout", jugglerloop.Result{End: jugglerloop.EndTimeout}, false, true, StateFailed, "timeout:"},
		{"tool error", jugglerloop.Result{End: jugglerloop.EndToolError}, false, true, StateFailed, "tool_error:"},
		{"model error", jugglerloop.Result{End: jugglerloop.EndModelError}, false, true, StateFailed, "model_error:"},
		{"holder cancel", jugglerloop.Result{End: jugglerloop.EndTimeout}, true, true, StateAborted, "cancelled"},
	}
	for _, tc := range cases {
		state, msg := AgentVerdict(tc.res, nil, tc.cancelled, tc.passed, nil)
		if state != tc.state || !strings.HasPrefix(msg, tc.prefix) {
			t.Errorf("%s: (%s, %q)", tc.name, state, msg)
		}
	}
}

func TestExtractURIsFromMCPContent(t *testing.T) {
	content := json.RawMessage(`[{"type":"text","text":"{\"url\":\"https://a\",\"uris\":[\"https://b\"]}"},{"type":"text","text":"not json"},{"type":"resource_link","uri":"https://c"},{"type":"resource","resource":{"uri":"https://d"}}]`)
	got := ExtractURIsFromMCPContent("t", content)
	want := []string{"https://a", "https://b", "https://c", "https://d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("uris = %v", got)
	}
	if got := ExtractURIsFromMCPContent("t", json.RawMessage(`{"uri":"https://e"}`)); len(got) != 1 || got[0] != "https://e" {
		t.Errorf("object content uris = %v", got)
	}
	if got := ExtractURIsFromMCPContent("t", json.RawMessage(`[]`)); got == nil {
		t.Error("result must never be nil")
	}
}

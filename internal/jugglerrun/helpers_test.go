package jugglerrun

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
	"code.linenisgreat.com/clown/internal/jugglerbrief"
	"code.linenisgreat.com/clown/internal/jugglerrun/jugglerruntest"
)

func TestMain(m *testing.M) {
	jugglerruntest.RunFakeIfRequested()
	os.Exit(m.Run())
}

const testRoot = "root-1"

// testTemplateTOML is an issue-filer brief template: no principal, parent,
// room or task.
const testTemplateTOML = `schema = 1
model = "test-model"
system = "you file issues"
moxyfile = "# narrowed to ring"
tools = ["ring_create_issue"]

[env]
PEBBLE_TARGET_MODE = "shadow"

[evaluator]
kind = "jq"
program = '.cannot_complete == null and ([.calls[] | select(.kind == "issue" and .ok)] | length) >= 1'

[limits]
steps = 6
wall_clock = "30s"
`

const testTask = "file one issue"

func templateBytes(extraTopLevel ...string) []byte {
	return []byte(strings.Join(extraTopLevel, "\n") + "\n" + testTemplateTOML)
}

// briefBytes is the filled brief for principal.
func briefBytes(principal string) []byte {
	return templateBytes(fmt.Sprintf("principal = %q", principal), `parent = "root-1"`, `room = "run@rooms.test"`, fmt.Sprintf("task = %q", testTask))
}

func parsedBrief(t *testing.T, principal string) *jugglerbrief.Brief {
	t.Helper()
	b, err := jugglerbrief.Parse(briefBytes(principal))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type harness struct {
	fakes *jugglerruntest.Fakes
	deps  LifecycleDeps
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	f := jugglerruntest.Install(t)
	return &harness{fakes: f, deps: LifecycleDeps{
		Ringmaster: ExecRingmaster{Bin: f.Ringmaster},
		Troupe:     ExecTroupe{Bin: f.Troupe},
		Units:      ExecSystemdRun{Bin: f.SystemdRun},
		Store:      Store{Root: t.TempDir()},
	}}
}

func (h *harness) agentDeps(resolved rm.ResolveModelResult) AgentDeps {
	return AgentDeps{
		Ringmaster:   h.deps.Ringmaster,
		Troupe:       h.deps.Troupe,
		ResolveModel: func(context.Context, string) (rm.ResolveModelResult, error) { return resolved, nil },
	}
}

// scriptedOpenAI answers request N with replies[N] (the last repeats).
func scriptedOpenAI(t *testing.T, replies ...string) *httptest.Server {
	return jugglerruntest.ScriptedModel(t, replies...)
}

func openAIResolved(srv *httptest.Server) rm.ResolveModelResult {
	return rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL + "/v1", Token: "sk-test", Style: rm.StyleOpenAICompat}
}

const (
	openAICreateIssue    = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"ring_create_issue","arguments":"{\"title\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`
	openAIEndTurn        = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`
	openAICannotComplete = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_9","type":"function","function":{"name":"cannot_complete","arguments":"{\"reason\":\"no repo matches\"}"}}]},"finish_reason":"tool_calls"}]}`
)

const createIssueOK = `{"content":[{"type":"text","text":"{\"url\":\"https://code.example/clown/issues/301\"}"}],"isError":false}`

// fakeMoxy is an MCP streamable-HTTP server advertising ring_create_issue
// (kind issue) and ring_list_repos, whose create_issue result carries an
// issue URL in a text block.
func fakeMoxy(t *testing.T) *httptest.Server {
	t.Helper()
	return fakeMoxyWith(t, createIssueOK)
}

// fakeMoxyBin writes a fake moxy binary: it logs its cwd, argv, principal env
// and whether a moxyfile is in its cwd, prints a handshake for mcp, and
// blocks until its stdin closes.
func fakeMoxyBin(t *testing.T, mcp *httptest.Server) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = dir+"/moxy", dir+"/moxy.log"
	script := fmt.Sprintf(`#!/bin/sh
{ echo "cwd=$PWD"; for a in "$@"; do echo "arg=$a"; done; echo "sid=$CLOWN_SESSION_ID"; if [ -f moxyfile ]; then echo "moxyfile=yes"; fi; } >> '%s'
echo "1|1|tcp|%s|streamable-http"
read line
`, log, strings.TrimPrefix(mcp.URL, "http://"))
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

const ringTools = `[{"name":"ring_create_issue","description":"File an issue","inputSchema":{"type":"object"},"_meta":{"kind":"issue"}},{"name":"ring_list_repos","inputSchema":{"type":"object"}}]`

func fakeMoxyWith(t *testing.T, callResult string) *httptest.Server {
	return jugglerruntest.FakeMCP(t, ringTools, callResult)
}

func toJobRecords(recs []jugglerruntest.Record) []JobRecord {
	out := make([]JobRecord, len(recs))
	for i, r := range recs {
		out[i] = JobRecord{Job: r.Job, Type: r.Type, Message: r.Message, ResultRef: r.ResultRef, Source: r.Source, TS: r.TS}
	}
	return out
}

func countTerminals(recs []jugglerruntest.Record) int {
	n := 0
	for _, r := range recs {
		if IsTerminalState(r.Type) {
			n++
		}
	}
	return n
}

func lastRecord(t *testing.T, recs []jugglerruntest.Record) jugglerruntest.Record {
	t.Helper()
	if len(recs) == 0 {
		t.Fatal("no records")
	}
	return recs[len(recs)-1]
}

func hasArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

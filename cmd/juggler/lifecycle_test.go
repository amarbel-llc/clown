package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rm "code.linenisgreat.com/clown/internal/juggler"
	jr "code.linenisgreat.com/clown/internal/jugglerrun"
	"code.linenisgreat.com/clown/internal/jugglerrun/jugglerruntest"
)

func TestMain(m *testing.M) {
	jugglerruntest.RunFakeIfRequested()
	os.Exit(m.Run())
}

// lifecycleEnv installs the fakes, points the binary overrides and the state
// dir at them, and clears any ambient identity.
func lifecycleEnv(t *testing.T) *jugglerruntest.Fakes {
	t.Helper()
	f := jugglerruntest.Install(t)
	t.Setenv(jr.RingmasterBinEnv, f.Ringmaster)
	t.Setenv(jr.TroupeBinEnv, f.Troupe)
	t.Setenv(jr.SystemdRunBinEnv, f.SystemdRun)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(jr.SessionIDEnv, "")
	t.Setenv(jr.MoxyURLEnv, "")
	return f
}

const cmdBrief = `schema = 1
%s
parent = "%s"
room = "run@rooms.test"
model = "m"
system = "s"
task = "t"
tools = ["create_issue"]

[evaluator]
kind = "jq"
program = '.cannot_complete == null and ([.calls[] | select(.ok)] | length) >= 1'

[limits]
steps = 4
wall_clock = "20s"
`

func writeBrief(t *testing.T, principal, parent string) string {
	t.Helper()
	line := ""
	if principal != "" {
		line = fmt.Sprintf("principal = %q", principal)
	}
	path := filepath.Join(t.TempDir(), "brief.toml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(cmdBrief, line, parent)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func cmdModelServer(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		i := n
		if i >= len(replies) {
			i = len(replies) - 1
		}
		n++
		_, _ = io.WriteString(w, replies[i])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func cmdMoxyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := `{}`
		switch req.Method {
		case "tools/list":
			result = `{"tools":[{"name":"create_issue","inputSchema":{"type":"object"},"_meta":{"kind":"issue"}}]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"{\"url\":\"https://x/1\"}"}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const (
	cmdToolCallReply = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"create_issue","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	cmdEndTurnReply  = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`
)

func TestCmdRun_ExitCodeMirrorsTheVerdict(t *testing.T) {
	f := lifecycleEnv(t)
	model := cmdModelServer(t, cmdToolCallReply, cmdEndTurnReply)
	resolve := func(context.Context, string) (rm.ResolveModelResult, error) {
		return rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: model.URL + "/v1", Style: "openai-compat"}, nil
	}
	t.Setenv(jr.SessionIDEnv, "agent-1")
	t.Setenv(jr.MoxyURLEnv, cmdMoxyServer(t).URL)

	var out, errb bytes.Buffer
	code := cmdRun(context.Background(), resolve, model.Client(), []string{"--brief", writeBrief(t, "agent-1", "root-1"), "--brief-stanza", "b0"}, nil, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errb.String())
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["state"] != "succeeded" || got["ledger"] == "" {
		t.Fatalf("stdout = %s (%v)", out.String(), err)
	}
	if len(f.Wakes(t)) != 0 {
		t.Error("juggler run must not emit the exit wake")
	}

	// The same agent with no successful call fails the evaluator: exit 2.
	failing := cmdModelServer(t, cmdEndTurnReply)
	resolveFailing := func(context.Context, string) (rm.ResolveModelResult, error) {
		return rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: failing.URL + "/v1", Style: "openai-compat"}, nil
	}
	out.Reset()
	if code := cmdRun(context.Background(), resolveFailing, failing.Client(), []string{"--brief", writeBrief(t, "agent-1", "root-1")}, nil, &out, &errb); code != jr.ExitFailed {
		t.Fatalf("exit = %d, want 2; stdout %s", code, out.String())
	}
}

func TestCmdRun_UsageAndIdentityErrors(t *testing.T) {
	lifecycleEnv(t)
	var out, errb bytes.Buffer
	if code := cmdRun(context.Background(), nil, nil, nil, nil, &out, &errb); code != 1 {
		t.Errorf("no --brief: exit = %d", code)
	}
	t.Setenv(jr.SessionIDEnv, "intruder")
	if code := cmdRun(context.Background(), nil, nil, []string{"--brief", writeBrief(t, "agent-1", "root-1")}, nil, &out, &errb); code != 1 {
		t.Errorf("CLOWN_SESSION_ID mismatch: exit = %d", code)
	}
}

func TestCmdExitWake_ReadsServiceResultFromEnv(t *testing.T) {
	f := lifecycleEnv(t)
	rmc := jr.ExecRingmaster{Bin: f.Ringmaster}
	job, err := rmc.Start(context.Background(), "root-1", jr.AgentJobLabel, jr.SpawnSource)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"SERVICE_RESULT": "timeout", "EXIT_CODE": "killed", "EXIT_STATUS": "TERM", jr.SessionIDEnv: "agent-1"}
	var out, errb bytes.Buffer
	if code := cmdExitWake(func(k string) string { return env[k] }, []string{"--job", job, "--target", "root-1"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errb.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["state"] != "failed" || got["reason"] != "failed" || got["wrote_terminal"] != true {
		t.Fatalf("stdout = %s", out.String())
	}
	recs := f.Records(t, "root-1", job)
	if last := recs[len(recs)-1]; last.Type != "failed" || last.Message != "wall clock (RuntimeMaxSec) expired" {
		t.Errorf("terminal = %+v", last)
	}
	if code := cmdExitWake(func(string) string { return "" }, []string{"--job", job}, &out, &errb); code != 1 {
		t.Errorf("missing --target: exit = %d", code)
	}
}

func TestCmdSpawn_RunKeyIdempotencyWaitAndResolve(t *testing.T) {
	f := lifecycleEnv(t)
	input := filepath.Join(t.TempDir(), "rec.json")
	if err := os.WriteFile(input, []byte(`{"recording":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	newRunArgs := []string{"--new-run", "--run-key", "rec-1", "--input", input, "--issuer", "webhook", "--room", "run@rooms.test"}
	var out, errb bytes.Buffer
	if code := cmdSpawn(newRunArgs, nil, &out, &errb); code != 0 {
		t.Fatalf("new-run exit = %d, stderr = %s", code, errb.String())
	}
	var run jr.NewRunResult
	if err := json.Unmarshal(out.Bytes(), &run); err != nil || run.Existing || run.RunJob == "" || run.RootPrincipal == "" {
		t.Fatalf("new-run stdout = %s", out.String())
	}
	out.Reset()
	if code := cmdSpawn(newRunArgs, nil, &out, &errb); code != 0 {
		t.Fatal("second new-run failed")
	}
	var again jr.NewRunResult
	if err := json.Unmarshal(out.Bytes(), &again); err != nil || !again.Existing || again.RunJob != run.RunJob {
		t.Fatalf("second new-run stdout = %s", out.String())
	}

	out.Reset()
	brief := writeBrief(t, "", run.RootPrincipal)
	code := cmdSpawn([]string{"--brief", brief, "--wait", "--timeout", "100ms", "--juggler", "/bin/juggler", "--user"}, nil, &out, &errb)
	if code != jr.ExitWaitTimeout {
		t.Fatalf("--wait exit = %d, want 5 (no agent runs under the fake systemd-run); stderr %s", code, errb.String())
	}
	var waited jr.WaitRecord
	if err := json.Unmarshal(out.Bytes(), &waited); err != nil || waited.State != "running" || waited.JID == "" || waited.Job == "" {
		t.Fatalf("--wait stdout = %s", out.String())
	}
	if units := f.Calls(t, "systemd-run"); len(units) != 1 || units[0].Argv[0] != "--user" {
		t.Errorf("systemd-run calls = %+v", units)
	}

	out.Reset()
	if code := cmdSpawn([]string{"--brief", brief}, nil, &out, &errb); code != 0 {
		t.Fatal("idempotent spawn failed")
	}
	var launch map[string]string
	if err := json.Unmarshal(out.Bytes(), &launch); err != nil || launch["job"] != waited.Job || len(launch) != 3 {
		t.Fatalf("launch stdout = %s", out.String())
	}

	out.Reset()
	if code := cmdHandles([]string{waited.Job}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"holder":"`+run.RootPrincipal+`"`) {
		t.Errorf("handles exit = %d, stdout = %s", code, out.String())
	}

	out.Reset()
	if code := cmdResolve([]string{run.RunJob, "--state", "failed", "--reason", "no choice", "--fallback-artifacts", `[{"tool":"note","kind":"note","uris":["orgzly://1"]}]`}, &out, &errb); code != 0 {
		t.Fatalf("resolve exit = %d, stderr = %s", code, errb.String())
	}
	if wakes := f.Wakes(t); len(wakes) != 1 || wakes[0].Target != "webhook" {
		t.Errorf("wakes = %+v", wakes)
	}
	out.Reset()
	if code := cmdJobLedger([]string{run.RunJob}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"fallback"`) {
		t.Errorf("job-ledger exit = %d, stdout = %s", code, out.String())
	}
}

func TestCmdDecide_RunKeyRecordsRouteEntries(t *testing.T) {
	f := lifecycleEnv(t)
	input := filepath.Join(t.TempDir(), "rec.json")
	if err := os.WriteFile(input, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := cmdSpawn([]string{"--new-run", "--run-key", "rec-9", "--input", input, "--issuer", "webhook", "--room", "run@rooms.test"}, nil, &out, &errb); code != 0 {
		t.Fatalf("new-run: %s", errb.String())
	}
	var run jr.NewRunResult
	if err := json.Unmarshal(out.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, decideOK) }))
	t.Cleanup(srv.Close)
	decide := func(extra ...string) int {
		args := append([]string{"--model", "jev", "--room", run.Room, "--parent", "rec-1", "--troupe", f.Troupe, "--run-key", "rec-9"}, extra...)
		out.Reset()
		return cmdDecide(fakeResolver{model: decideModel{URL: srv.URL}}, srv.Client(), args, strings.NewReader(decideTestPayload), &out, &errb)
	}
	if code := decide(); code != 0 {
		t.Fatalf("usable decide exit = %d: %s", code, errb.String())
	}
	if code := decide("--min-confidence", "0.95"); code != 4 {
		t.Fatalf("below-threshold decide exit = %d", code)
	}

	out.Reset()
	if code := cmdJobLedger([]string{run.RunJob}, &out, &errb); code != 0 {
		t.Fatalf("job-ledger: %s", errb.String())
	}
	var ledger jr.RunLedger
	if err := json.Unmarshal(out.Bytes(), &ledger); err != nil || len(ledger.Calls) != 2 {
		t.Fatalf("run ledger = %s (%v)", out.String(), err)
	}
	usable, below := ledger.Calls[0], ledger.Calls[1]
	if usable.Tool != "route" || usable.Kind != "route" || !usable.OK || usable.Choice != "issue" || usable.Verdict != "usable" ||
		usable.Confidence == nil || *usable.Confidence != 0.81 || usable.Threshold == nil || *usable.Threshold != 0.5 || !strings.HasPrefix(usable.StanzaID, "chat-") {
		t.Errorf("usable route entry = %+v", usable)
	}
	if below.OK || below.Verdict != "below-threshold" || below.Reason == "" || *below.Threshold != 0.95 {
		t.Errorf("below-threshold route entry = %+v", below)
	}

	if code := cmdResolve([]string{run.RunJob, "--state", "failed", "--reason", "below threshold"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	out.Reset()
	_ = cmdJobLedger([]string{run.RunJob}, &out, &errb)
	if err := json.Unmarshal(out.Bytes(), &ledger); err != nil || len(ledger.Calls) != 3 || ledger.Calls[2].Tool != "fallback" {
		t.Fatalf("resolved ledger = %s", out.String())
	}
	if code := decide(); code != 1 {
		t.Errorf("a resolved run's ledger is closed: decide exit = %d", code)
	}
	if code := cmdDecide(fakeResolver{}, nil, []string{"--model", "m", "--room", "r", "--parent", "p", "--run-key", "nope"}, strings.NewReader(decideTestPayload), &out, &errb); code != 1 {
		t.Errorf("unknown run key: exit = %d", code)
	}
}

func TestCmdSpawn_UsageErrors(t *testing.T) {
	lifecycleEnv(t)
	for name, args := range map[string][]string{
		"neither":          {},
		"both":             {"--new-run", "--brief", "b"},
		"new-run no input": {"--new-run", "--room", "r"},
		"wait on new-run":  {"--new-run", "--input", "-", "--room", "r", "--wait"},
		"timeout no wait":  {"--brief", "b", "--timeout", "1s"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := cmdSpawn(args, strings.NewReader(""), &out, &errb); code != 1 {
				t.Errorf("exit = %d", code)
			}
		})
	}
	var out, errb bytes.Buffer
	code := cmdSpawn([]string{"--new-run", "--input", "-", "--issuer", "w", "--room-domain", "rooms.test"}, strings.NewReader("x"), &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "troupe's lane") {
		t.Errorf("room creation: exit = %d, stderr = %s", code, errb.String())
	}
}

func TestCmdResolve_Usage(t *testing.T) {
	lifecycleEnv(t)
	var out, errb bytes.Buffer
	if code := cmdResolve([]string{"--state", "failed"}, &out, &errb); code != 1 {
		t.Errorf("exit = %d", code)
	}
	if code := cmdResolve([]string{"job-1", "--state", "failed", "--reason", "r", "--fallback-artifacts", "nope"}, &out, &errb); code != 1 {
		t.Errorf("bad artifacts: exit = %d", code)
	}
}

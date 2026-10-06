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
	// The minter credential resolve's teardown requires (the fake ignores it).
	t.Setenv(minterPasswordFileEnv, filepath.Join(t.TempDir(), "minter.pw"))
	t.Setenv(minterUserEnv, "troupe-minter")
	return f
}

func TestCmdSpawn_FailedNewRunPrintsThePendingRun(t *testing.T) {
	f := lifecycleEnv(t)
	f.Fail(t, "ringmaster", "start")
	args := []string{"--new-run", "--run-key", "rec-p", "--input", "-", "--issuer", "webhook", "--room", "run@rooms.test"}
	var out, errb bytes.Buffer
	if code := cmdSpawn(args, strings.NewReader("{}"), &out, &errb); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	var p map[string]any
	if err := json.Unmarshal(out.Bytes(), &p); err != nil || p["run_key"] != "rec-p" || p["pending"] != true || p["root_principal"] != jr.NewRootPrincipal("rec-p") || p["room"] != "run@rooms.test" {
		t.Fatalf("stdout = %q (%v)", out.String(), err)
	}
	if !strings.Contains(errb.String(), "starting the run job") {
		t.Errorf("stderr must carry the cause: %s", errb.String())
	}

	// decide on the pending run refuses before any model call.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = io.WriteString(w, decideOK) }))
	t.Cleanup(srv.Close)
	out.Reset()
	errb.Reset()
	code := cmdDecide(decisionsAt(srv.URL), srv.Client(), []string{"--model", "jev", "--room", "run@rooms.test", "--parent", "p", "--troupe", f.Troupe, "--run-key", "rec-p"}, strings.NewReader(decideTestPayload), &out, &errb)
	if code != 1 || calls != 0 || !strings.Contains(errb.String(), "pending") {
		t.Errorf("decide on a pending run: exit = %d, model calls = %d, stderr = %s", code, calls, errb.String())
	}
	for _, c := range f.Calls(t, "troupe") {
		if c.Argv[0] == "muc" && c.Argv[1] == "send" && strings.Contains(strings.Join(c.Argv, " "), `"decision"`) {
			t.Errorf("no decision stanza may be posted: %q", c.Argv)
		}
	}

	// The retry resumes: existing false, resumed true.
	if err := os.Remove(f.Dir + "/fail/ringmaster-start"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdSpawn(args, strings.NewReader("{}"), &out, &errb); code != 0 || !strings.Contains(out.String(), `"existing":false`) || !strings.Contains(out.String(), `"resumed":true`) {
		t.Fatalf("retry exit = %d, stdout = %s", code, out.String())
	}
	out.Reset()
	if code := cmdSpawn(args, strings.NewReader("{}"), &out, &errb); code != 0 || !strings.Contains(out.String(), `"existing":true`) || strings.Contains(out.String(), `"resumed"`) {
		t.Fatalf("complete run: exit = %d, stdout = %s", code, out.String())
	}
}

func TestCmdResolve_RequiresTheMinterCredential(t *testing.T) {
	f := lifecycleEnv(t)
	var out, errb bytes.Buffer
	if code := cmdSpawn([]string{"--new-run", "--run-key", "rec-m", "--input", "-", "--issuer", "webhook", "--room", "run@rooms.test"}, strings.NewReader("{}"), &out, &errb); code != 0 {
		t.Fatalf("new-run: %s", errb.String())
	}
	var run jr.NewRunResult
	if err := json.Unmarshal(out.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	t.Setenv(minterPasswordFileEnv, "")
	t.Setenv(minterUserEnv, "")
	before := len(f.Calls(t, ""))
	out.Reset()
	errb.Reset()
	if code := cmdResolve([]string{run.RunJob, "--state", "succeeded", "--reason", "r", "--result-line", "x", "--canary-room", "canary@rooms.test"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), minterPasswordFileEnv) {
		t.Fatalf("exit = %d, stderr = %s", code, errb.String())
	}
	if n := len(f.Calls(t, "")); n != before {
		t.Errorf("nothing may run before the refusal: %d calls became %d", before, n)
	}
	out.Reset()
	if code := cmdResolve([]string{run.RunJob, "--state", "succeeded", "--reason", "r", "--keep-accounts"}, &out, &errb); code != 0 {
		t.Errorf("--keep-accounts needs no minter credential: exit = %d, stderr = %s", code, errb.String())
	}
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

func cmdMoxyServer(t *testing.T) *httptest.Server {
	return jugglerruntest.FakeMCP(t,
		`[{"name":"create_issue","inputSchema":{"type":"object"},"_meta":{"kind":"issue"}}]`,
		`{"content":[{"type":"text","text":"{\"url\":\"https://x/1\"}"}]}`)
}

const (
	cmdToolCallReply = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"create_issue","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	cmdEndTurnReply  = `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`
)

func TestCmdRun_ExitCodeMirrorsTheVerdict(t *testing.T) {
	f := lifecycleEnv(t)
	model := jugglerruntest.ScriptedModel(t, cmdToolCallReply, cmdEndTurnReply)
	resolve := resolvedAs(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: model.URL + "/v1", Style: rm.StyleOpenAICompat})
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
	failing := jugglerruntest.ScriptedModel(t, cmdEndTurnReply)
	resolveFailing := resolvedAs(rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: failing.URL + "/v1", Style: rm.StyleOpenAICompat})
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
	code := cmdSpawn([]string{"--brief", brief, "--wait", "--timeout", "100ms", "--stop-grace", "200ms", "--juggler", "/bin/juggler", "--user"}, nil, &out, &errb)
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
	if code := cmdResolve([]string{run.RunJob, "--state", "failed", "--reason", "no choice", "--stop-grace", "200ms", "--fallback-artifacts", `[{"tool":"note","kind":"note","uris":["orgzly://1"]}]`}, &out, &errb); code != 0 {
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

func TestCmdResolve_StopGraceLongerThanTheBaseBudget(t *testing.T) {
	lifecycleEnv(t)
	input := filepath.Join(t.TempDir(), "rec.json")
	if err := os.WriteFile(input, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := cmdSpawn([]string{"--new-run", "--run-key", "rec-7", "--input", input, "--issuer", "webhook", "--room", "run@rooms.test"}, nil, &out, &errb); code != 0 {
		t.Fatalf("new-run: %s", errb.String())
	}
	var run jr.NewRunResult
	if err := json.Unmarshal(out.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdResolve([]string{run.RunJob, "--state", "succeeded", "--reason", "done", "--stop-grace", "90s"}, &out, &errb); code != 0 {
		t.Fatalf("resolve exit = %d, stderr = %s", code, errb.String())
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
		return cmdDecide(decisionsAt(srv.URL), srv.Client(), args, strings.NewReader(decideTestPayload), &out, &errb)
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
	// fallback, then the root's account teardown (the run has no children).
	if err := json.Unmarshal(out.Bytes(), &ledger); err != nil || len(ledger.Calls) != 4 || ledger.Calls[2].Tool != "fallback" ||
		ledger.Calls[3].Tool != jr.TeardownTool || !ledger.Calls[3].OK {
		t.Fatalf("resolved ledger = %s", out.String())
	}
	if code := decide(); code != 1 {
		t.Errorf("a resolved run's ledger is closed: decide exit = %d", code)
	}
	if code := cmdDecide(mustNotResolve(t), nil, []string{"--model", "m", "--room", "r", "--parent", "p", "--run-key", "nope"}, strings.NewReader(decideTestPayload), &out, &errb); code != 1 {
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
	if code != 1 || !strings.Contains(errb.String(), "--operator-jid") {
		t.Errorf("room creation without an operator: exit = %d, stderr = %s", code, errb.String())
	}
}

func TestCmdSpawn_RoomDomainWithTheOperatorFromTheEnvironment(t *testing.T) {
	f := lifecycleEnv(t)
	t.Setenv(operatorJIDEnv, "operator@xmpp.test")
	var out, errb bytes.Buffer
	if code := cmdSpawn([]string{"--new-run", "--run-key", "rk-9", "--input", "-", "--issuer", "w", "--room-domain", "rooms.test"}, strings.NewReader("x"), &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errb.String())
	}
	var run jr.NewRunResult
	if err := json.Unmarshal(out.Bytes(), &run); err != nil || run.Room != "rk-9@rooms.test" || run.OperatorJID != "operator@xmpp.test" {
		t.Fatalf("stdout = %s", out.String())
	}
	if room, ok := f.Room(t, run.Room); !ok || room.Affiliations["operator@xmpp.test"] != jr.AffiliationOwner {
		t.Errorf("room = %+v", room)
	}

	out.Reset()
	code := cmdResolve([]string{run.RunJob, "--state", "succeeded", "--reason", "r", "--result-line", "rk-9 done", "--canary-room", "canary@rooms.test"}, &out, &errb)
	if code != 0 || !strings.Contains(out.String(), `"torn_down":true`) {
		t.Fatalf("resolve exit = %d, stdout = %s, stderr = %s", code, out.String(), errb.String())
	}
	if posts := f.MUC(t); posts[len(posts)-1].Room != "canary@rooms.test" || posts[len(posts)-1].Subject != "rk-9 done" {
		t.Errorf("canary post = %+v", posts[len(posts)-1])
	}
}

func TestCmdResolve_CanaryAndKeepAccountsFlags(t *testing.T) {
	lifecycleEnv(t)
	var out, errb bytes.Buffer
	for name, args := range map[string][]string{
		"line without room": {"job-1", "--state", "failed", "--reason", "r", "--result-line", "x"},
		"room without line": {"job-1", "--state", "failed", "--reason", "r", "--canary-room", "c@rooms.test"},
		"multi-line":        {"job-1", "--state", "failed", "--reason", "r", "--result-line", "a\nb", "--canary-room", "c@rooms.test"},
	} {
		if code := cmdResolve(args, &out, &errb); code != 1 {
			t.Errorf("%s: exit = %d", name, code)
		}
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

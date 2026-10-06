package jugglerrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/jugglerloop"
)

const testIssuer = "pebble-webhook"

func newRun(t *testing.T, h *harness, key string) NewRunResult {
	t.Helper()
	res, err := NewRun(context.Background(), h.deps, NewRunRequest{RunKey: key, Issuer: testIssuer, Input: []byte(`{"recording":"r1"}`), Room: "run@rooms.test"})
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	return res
}

// pinPrincipals makes deps mint principals[0] as every run root and the rest
// as children, in order (cycling; principals[0] when there are none).
func pinPrincipals(h *harness, principals ...string) {
	h.deps.NewRootPrincipal = func(string) string { return principals[0] }
	children := principals[1:]
	if len(children) == 0 {
		children = principals[:1]
	}
	i := 0
	h.deps.NewPrincipal = func() string {
		p := children[i%len(children)]
		i++
		return p
	}
}

func TestNewRootPrincipal_IsUUIDv5OfTheRunKey(t *testing.T) {
	// RFC 9562 Appendix A.4: the DNS namespace and "www.example.com".
	if got := uuidV5(mustParseUUID("6ba7b810-9dad-11d1-80b4-00c04fd430c8"), "www.example.com"); got != "2ed6657d-e927-568b-95e1-2665a8aea6a2" {
		t.Errorf("uuidV5 = %s", got)
	}
	a, b := NewRootPrincipal("rec-42"), NewRootPrincipal("rec-42")
	if a != b || a == NewRootPrincipal("rec-43") || a[14] != '5' {
		t.Errorf("root principals = %s, %s", a, b)
	}
}

func TestNewRun_CreatesAndIsIdempotentOnRunKey(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newRun(t, h, "rec-42")
	if res.Existing || res.RunKey != "rec-42" || res.RootPrincipal != testRoot || res.RootJID != testRoot+"@xmpp.test" ||
		res.Room != "run@rooms.test" || res.RootCredentialRef == "" || res.RunJob == "" {
		t.Fatalf("result = %+v", res)
	}
	recs := h.fakes.Records(t, testIssuer, res.RunJob)
	if recs[0].Type != "started" || len(HoldersFromRecords(toJobRecords(recs))) != 1 {
		t.Errorf("run job journal = %+v", recs)
	}
	posts := h.fakes.MUC(t)
	if len(posts) != 1 || posts[0].User != testRoot || posts[0].Session != testRoot || posts[0].From != testRoot {
		t.Fatalf("root stanza must be posted as the run root: %+v", posts)
	}
	var stanza map[string]any
	if err := json.Unmarshal([]byte(posts[0].Subject), &stanza); err != nil || stanza["type"] != "run_input" || stanza["input"].(map[string]any)["recording"] != "r1" {
		t.Errorf("root stanza = %s (%v)", posts[0].Subject, err)
	}
	if _, err := os.Stat(h.deps.Store.runPath("rec-42")); err != nil {
		t.Errorf("run record: %v", err)
	}

	calls := len(h.fakes.Calls(t, ""))
	again := newRun(t, h, "rec-42")
	if !again.Existing || again.RunJob != res.RunJob || again.RootPrincipal != res.RootPrincipal {
		t.Errorf("second result = %+v", again)
	}
	if n := len(h.fakes.Calls(t, "")); n != calls {
		t.Errorf("an existing run key must create nothing: %d calls became %d", calls, n)
	}
}

func TestNewRun_ExistingRunReportsResolvedAndTornDown(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newRun(t, h, "rec-42")
	if res.Resolved || res.TornDown {
		t.Fatalf("a fresh run is neither resolved nor torn down: %+v", res)
	}
	if again := newRun(t, h, "rec-42"); !again.Existing || again.Resolved || again.TornDown {
		t.Fatalf("existing unresolved run = %+v", again)
	}
	if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"}); err != nil {
		t.Fatal(err)
	}
	again := newRun(t, h, "rec-42")
	if !again.Existing || !again.Resolved || !again.TornDown {
		t.Errorf("repeated run key after Resolve = %+v", again)
	}
	b, _ := json.Marshal(again)
	if !containsAll(string(b), `"resolved":true`, `"torn_down":true`, `"existing":true`) {
		t.Errorf("JSON = %s", b)
	}
}

// callsExcept is the run ledger's entries without those of tool.
func callsExcept(l RunLedger, tool string) []RunLedgerEntry {
	var out []RunLedgerEntry
	for _, e := range l.Calls {
		if e.Tool != tool {
			out = append(out, e)
		}
	}
	return out
}

// callsOf is the run ledger's entries of tool.
func callsOf(l RunLedger, tool string) []RunLedgerEntry {
	var out []RunLedgerEntry
	for _, e := range l.Calls {
		if e.Tool == tool {
			out = append(out, e)
		}
	}
	return out
}

func TestMint_PerIdentityPasswordFiles(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	rootPW := h.deps.Store.runDir("rec-42") + "/" + testRoot + ".pw"
	if res.RootCredentialRef != rootPW {
		t.Errorf("root credential ref = %q, want %q", res.RootCredentialRef, rootPW)
	}
	if fi, err := os.Stat(h.deps.Store.runDir("rec-42")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("run dir = %v, %v", fi, err)
	}
	rec := spawnChild(t, h)
	childPW := h.deps.Store.runDir("rec-42") + "/child-1.pw"
	if rec.CredentialRef != childPW {
		t.Errorf("child credential ref = %q, want %q", rec.CredentialRef, childPW)
	}
	var mints [][]string
	for _, c := range h.fakes.Calls(t, "troupe") {
		if c.Argv[0] == "mint" {
			mints = append(mints, c.Argv)
		}
	}
	want := [][]string{
		{"mint", "--session-key", testRoot, "--password-file", rootPW},
		{"mint", "--session-key", "child-1", "--password-file", childPW},
	}
	if strings.Join(flatten(mints), "|") != strings.Join(flatten(want), "|") {
		t.Errorf("mint argv = %q, want %q", mints, want)
	}
	units := h.fakes.Calls(t, "systemd-run")
	if !hasArg(units[0].Argv, "--setenv=TROUPE_XMPP_PASSWORD_FILE="+childPW) {
		t.Errorf("the unit must read the child's own file: %q", units[0].Argv)
	}
	for _, p := range []string{rootPW, childPW} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a successful spawn keeps %s: %v", p, err)
		}
	}
}

func TestSpawnChild_FailureRevokesTheChildsOwnFile(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	h.fakes.Fail(t, "systemd-run", "launch")
	if _, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"}); err == nil {
		t.Fatal("want an error")
	}
	childPW := h.deps.Store.runDir("rec-42") + "/child-1.pw"
	var revoke []string
	for _, c := range h.fakes.Calls(t, "troupe") {
		if c.Argv[0] == "mint-revoke" {
			revoke = c.Argv
		}
	}
	if strings.Join(revoke, "|") != strings.Join([]string{"mint-revoke", "--session-key", "child-1", "--password-file", childPW}, "|") {
		t.Errorf("mint-revoke argv = %q", revoke)
	}
	if _, err := os.Stat(h.deps.Store.runDir("rec-42") + "/" + testRoot + ".pw"); err != nil {
		t.Errorf("the root's credential must survive a child's failure: %v", err)
	}
}

func flatten(argvs [][]string) []string {
	var out []string
	for _, a := range argvs {
		out = append(out, strings.Join(a, " "))
	}
	return out
}

func TestResolve_StopsLiveChildren(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1", "child-2")
	res := newRun(t, h, "rec-42")
	live := spawnChild(t, h)
	done, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte("second"), RunKey: "rec-42", JugglerBin: "/bin/juggler"})
	if err != nil {
		t.Fatal(err)
	}
	h.fakes.AppendRecord(t, done.Parent, done.Job, StateSucceeded, "evaluator passed", "")
	go func() {
		for len(h.fakes.Cancelled(t)) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		h.fakes.AppendRecord(t, live.Parent, live.Job, StateAborted, "cancelled by a holder", "")
	}()

	if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done", StopGrace: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if got := h.fakes.Cancelled(t); len(got) != 1 || got[0] != live.Job {
		t.Errorf("only the live child may be cancelled: %v", got)
	}
	if term := lastRecord(t, h.fakes.Records(t, testIssuer, res.RunJob)); term.Type != StateSucceeded {
		t.Errorf("run terminal = %+v", term)
	}
	if countTerminals(h.fakes.Records(t, live.Parent, live.Job)) != 1 {
		t.Error("the live child must terminalize exactly once")
	}
	var ledger RunLedger
	data, err := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	if calls := callsExcept(ledger, TeardownTool); len(calls) != 2 || calls[0].Job != live.Job || !calls[0].StoppedByResolve || !calls[0].OK || calls[1].Tool != "fallback" {
		t.Errorf("run ledger = %s", data)
	}
}

func TestResolve_ChildThatIgnoresTheCancelIsRecordedNotStopped(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	live := spawnChild(t, h)
	if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateFailed, Reason: "x", StopGrace: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got := h.fakes.Cancelled(t); len(got) != 1 || got[0] != live.Job {
		t.Errorf("cancelled = %v", got)
	}
	var ledger RunLedger
	data, _ := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	if calls := callsExcept(ledger, TeardownTool); len(calls) != 2 || calls[0].OK || !calls[0].StoppedByResolve {
		t.Errorf("run ledger = %s", data)
	}
}

// flakyRingmaster fails WaitTerminal immediately and/or Records for one job.
type flakyRingmaster struct {
	ExecRingmaster
	waitErr   error
	recordsOf string
}

func (f flakyRingmaster) WaitTerminal(ctx context.Context, target, job string, timeout time.Duration) error {
	if f.waitErr != nil {
		return f.waitErr
	}
	return f.ExecRingmaster.WaitTerminal(ctx, target, job, timeout)
}

func (f flakyRingmaster) Records(ctx context.Context, target, job string) ([]JobRecord, error) {
	if job == f.recordsOf {
		return nil, errors.New("journal exploded")
	}
	return f.ExecRingmaster.Records(ctx, target, job)
}

func TestWaitChild_NonTimeoutWaitFailureDoesNotCancel(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	live := spawnChild(t, h)
	h.deps.Ringmaster = flakyRingmaster{ExecRingmaster: h.deps.Ringmaster.(ExecRingmaster), waitErr: errors.New("exec failed")}

	_, err := WaitChild(context.Background(), h.deps, live, time.Minute, time.Second)
	if err == nil {
		t.Fatal("want the wait error surfaced")
	}
	if got := h.fakes.Cancelled(t); len(got) != 0 {
		t.Errorf("a healthy job must not be cancelled: %v", got)
	}
}

func TestResolve_UnreadableChildJournalIsRecordedNotFatal(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	live := spawnChild(t, h)
	h.deps.Ringmaster = flakyRingmaster{ExecRingmaster: h.deps.Ringmaster.(ExecRingmaster), recordsOf: live.Job}

	if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateFailed, Reason: "x", StopGrace: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got := h.fakes.Cancelled(t); len(got) != 1 || got[0] != live.Job {
		t.Errorf("the cancel is still attempted: %v", got)
	}
	var ledger RunLedger
	data, _ := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	if calls := callsExcept(ledger, TeardownTool); len(calls) != 2 || calls[0].OK || calls[0].Tool != "subagent_stop" || !strings.Contains(calls[0].Reason, "journal unreadable: ") {
		t.Errorf("run ledger = %s", data)
	}
}

func TestNewRun_RoomDomainRequiresAnOperator(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	_, err := NewRun(context.Background(), h.deps, NewRunRequest{RunKey: "k", Issuer: testIssuer, Input: []byte("x"), RoomDomain: "rooms.test"})
	if err == nil || !strings.Contains(err.Error(), "operator JID") {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.fakes.Calls(t, "")); n != 0 {
		t.Errorf("nothing may be minted or created: %d calls", n)
	}
}

func TestNewRun_FailureAfterPostCleansUp(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	h.fakes.Fail(t, "ringmaster", "start")
	if _, err := NewRun(context.Background(), h.deps, NewRunRequest{RunKey: "k", Issuer: testIssuer, Input: []byte("x"), Room: "run@rooms.test"}); err == nil {
		t.Fatal("want an error")
	}
	posts := h.fakes.MUC(t)
	if len(posts) != 2 || !strings.Contains(posts[1].Subject, `"type":"withdrawn"`) || !strings.Contains(posts[1].Subject, posts[0].ID) {
		t.Errorf("the root stanza must be withdrawn: %+v", posts)
	}
	if len(h.fakes.Revoked(t)) != 0 {
		t.Error("the root is kept for the retry")
	}
	rec, _ := h.deps.Store.LoadRun("k")
	if rec == nil || !rec.Pending || rec.RootPrincipal != testRoot || rec.RunJob != "" || rec.RootStanza != "" {
		t.Fatalf("a pending run record must remain: %+v", rec)
	}
	if _, err := os.Stat(rec.RootCredentialRef); err != nil {
		t.Errorf("the root's password file is kept: %v", err)
	}
	if _, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "k", JugglerBin: "/bin/juggler"}); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Errorf("a pending run takes no agents: %v", err)
	}
}

func spawnChild(t *testing.T, h *harness) *ChildRecord {
	t.Helper()
	rec, existing, err := SpawnChild(context.Background(), h.deps, SpawnRequest{
		Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", MoxyURL: "http://127.0.0.1:9/mcp", JugglerBin: "/bin/juggler",
		UnitEnv: map[string]string{"XDG_STATE_HOME": "/state", SessionIDEnv: "must-not-pass"},
	})
	if err != nil || existing {
		t.Fatalf("SpawnChild: %v (existing=%v)", err, existing)
	}
	return rec
}

func TestSpawnChild_LaunchesTheUnit(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	t.Setenv(SessionIDEnv, testRoot)
	t.Setenv("TROUPE_XMPP_USER", testRoot)

	rec := spawnChild(t, h)
	if rec.Principal != "child-1" || rec.JID != "child-1@xmpp.test" || rec.Parent != testRoot || rec.Room != "run@rooms.test" || rec.Unit != "juggler-agent-child-1" {
		t.Fatalf("record = %+v", rec)
	}
	if got := rec.Launch(); got.JID != rec.JID || got.Job != rec.Job || got.Room != rec.Room {
		t.Errorf("launch = %+v", got)
	}

	staged, err := os.ReadFile(rec.BriefPath)
	if err != nil || !containsAll(string(staged), `principal = "child-1"`, `parent = "root-1"`, `room = "run@rooms.test"`, `task = "file one issue"`) {
		t.Errorf("the template must be filled: %s (%v)", staged, err)
	}
	if HoldersFromRecords(toJobRecords(h.fakes.Records(t, testRoot, rec.Job)))[0].Principal != testRoot {
		t.Error("the spawner must be the first holder on the child job")
	}

	posts := h.fakes.MUC(t)
	brief := posts[len(posts)-1]
	if brief.ID != rec.BriefStanza || brief.Session != testRoot || !strings.Contains(brief.Subject, `"type":"brief"`) {
		t.Errorf("brief stanza = %+v", brief)
	}

	units := h.fakes.Calls(t, "systemd-run")
	if len(units) != 1 {
		t.Fatalf("systemd-run calls = %d", len(units))
	}
	argv := units[0].Argv
	for _, want := range []string{
		"--unit", "juggler-agent-child-1", "--collect", "--property=Type=exec", "--property=RuntimeMaxSec=60", "--property=Delegate=yes",
		"--property=ExecStopPost=/bin/juggler exit-wake --job " + rec.Job + " --target " + testRoot,
		"--setenv=CLOWN_SESSION_ID=child-1", "--setenv=TROUPE_XMPP_USER=child-1", "--setenv=TROUPE_XMPP_PASSWORD_FILE=" + rec.CredentialRef,
		"--setenv=TROUPE_XMPP_DOMAIN=xmpp.test", "--setenv=JUGGLER_MOXY_URL=http://127.0.0.1:9/mcp", "--setenv=XDG_STATE_HOME=/state",
		"--setenv=PEBBLE_TARGET_MODE=shadow",
	} {
		if !hasArg(argv, want) {
			t.Errorf("systemd-run argv lacks %q: %q", want, argv)
		}
	}
	tail := strings.Join(argv[len(argv)-8:], " ")
	if tail != "/bin/juggler run --brief "+rec.BriefPath+" --job "+rec.Job+" --brief-stanza "+rec.BriefStanza {
		t.Errorf("unit command = %q", tail)
	}
	if hasArg(argv, "--setenv=CLOWN_SESSION_ID=must-not-pass") {
		t.Error("an identity variable leaked through UnitEnv")
	}
	if _, ok := units[0].Env[SessionIDEnv]; ok {
		t.Errorf("systemd-run client env carries the spawner's principal: %v", units[0].Env)
	}
	if _, ok := units[0].Env["TROUPE_XMPP_USER"]; ok {
		t.Errorf("systemd-run client env carries the spawner's XMPP identity: %v", units[0].Env)
	}

	again, existing, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"})
	if err != nil || !existing || again.Job != rec.Job {
		t.Errorf("same brief digest must return the existing child: %+v %v %v", again, existing, err)
	}
	if n := len(h.fakes.Calls(t, "systemd-run")); n != 1 {
		t.Errorf("systemd-run calls = %d after an idempotent spawn", n)
	}
	other, existing, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte("another task"), RunKey: "rec-42", JugglerBin: "/bin/juggler"})
	if err != nil || existing || other.Job == rec.Job {
		t.Errorf("a different task is a different subagent: %+v %v %v", other, existing, err)
	}
}

func TestSpawnChild_FindsTheRun(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1", "child-2")
	newRun(t, h, "rec-42")
	byRoom, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(`room = "run@rooms.test"`), Task: []byte(testTask), JugglerBin: "/bin/juggler"})
	if err != nil || byRoom.RunKey != "rec-42" {
		t.Fatalf("by room: %+v, %v", byRoom, err)
	}
	byRoot, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte("t2"), EnvPrincipal: testRoot, JugglerBin: "/bin/juggler"})
	if err != nil || byRoot.RunKey != "rec-42" {
		t.Fatalf("by root principal: %+v, %v", byRoot, err)
	}
	if _, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte("t3"), JugglerBin: "/bin/juggler"}); err == nil {
		t.Error("no run key, room or root principal must be an error")
	}
}

func TestSpawnChild_RejectsAForeignParent(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, "other-root")
	newRun(t, h, "rec-42")
	_, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(`parent = "root-1"`), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"})
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("err = %v", err)
	}
	if len(h.fakes.Calls(t, "troupe")) != 2 { // the run's mint + root stanza only
		t.Error("nothing may be minted for a rejected brief")
	}
}

func TestSpawnChild_LaunchFailureCleansUpAndRetrySucceeds(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1", "child-2")
	newRun(t, h, "rec-42")
	h.fakes.Fail(t, "systemd-run", "launch")

	_, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"})
	if err == nil || !strings.Contains(err.Error(), "transient unit") {
		t.Fatalf("err = %v", err)
	}
	if got := h.fakes.Revoked(t); len(got) != 1 || got[0] != "child-1" {
		t.Errorf("revoked = %v", got)
	}
	jobs := h.fakes.Jobs(t, testRoot)
	if len(jobs) != 1 {
		t.Fatalf("jobs on the root's channel = %v", jobs)
	}
	if term := lastRecord(t, h.fakes.Records(t, testRoot, jobs[0])); term.Type != StateFailed || !strings.HasPrefix(term.Message, "juggler spawn failed: ") {
		t.Errorf("the orphan job must be terminalized: %+v", term)
	}
	posts := h.fakes.MUC(t)
	if last := posts[len(posts)-1]; !strings.Contains(last.Subject, `"type":"withdrawn"`) || !strings.Contains(last.Subject, posts[len(posts)-2].ID) {
		t.Errorf("the brief stanza must be withdrawn: %+v", last)
	}
	digest := BriefDigest(templateBytes(), []byte(testTask))
	if rec, _ := h.deps.Store.LoadChild("rec-42", digest); rec != nil {
		t.Error("no child record may remain")
	}
	if _, err := os.Stat(h.deps.Store.stagedBriefPath("rec-42", digest)); !os.IsNotExist(err) {
		t.Errorf("staged brief must be removed: %v", err)
	}

	if err := os.Remove(h.fakes.Dir + "/fail/systemd-run-launch"); err != nil {
		t.Fatal(err)
	}
	rec, existing, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"})
	if err != nil || existing || rec.Principal != "child-2" {
		t.Fatalf("retry: %+v %v %v", rec, existing, err)
	}
}

func TestSpawnChild_RefusesAResolvedRun(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newRun(t, h, "rec-42")
	if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"}); err == nil {
		t.Fatal("a resolved run must not take new agents")
	}
}

// finishAgent stands in for `juggler run` inside the unit.
func finishAgent(t *testing.T, h *harness, rec *ChildRecord, state, message string, l jugglerloop.Ledger) {
	t.Helper()
	doc, err := marshalDocument(l)
	if err != nil {
		t.Fatal(err)
	}
	spool := h.fakes.SpoolPath(rec.Parent, rec.Job)
	if err := os.WriteFile(spool, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	h.fakes.AppendRecord(t, rec.Parent, rec.Job, state, message, spool)
}

func TestWaitChild(t *testing.T) {
	issue := "issue"
	succeeded := jugglerloop.Ledger{Schema: 1, Calls: []jugglerloop.LedgerCall{
		{Tool: "list_repos", OK: true, URIs: []string{}},
		{Tool: "create_issue", Kind: &issue, OK: true, URIs: []string{"https://code.example/clown/issues/301"}},
	}, End: jugglerloop.LedgerEnd{Reason: jugglerloop.EndTurn}}
	cannot := jugglerloop.Ledger{Schema: 1, Calls: []jugglerloop.LedgerCall{}, End: jugglerloop.LedgerEnd{Reason: jugglerloop.EndCannotComplete}, CannotComplete: &jugglerloop.CannotComplete{Reason: "no repo matches"}}

	t.Run("succeeded", func(t *testing.T) {
		h := newHarness(t)
		pinPrincipals(h, testRoot, "child-1")
		newRun(t, h, "rec-42")
		rec := spawnChild(t, h)
		time.AfterFunc(50*time.Millisecond, func() { finishAgent(t, h, rec, StateSucceeded, "evaluator passed", succeeded) })
		w, err := WaitChild(context.Background(), h.deps, rec, 5*time.Second, 0)
		if err != nil {
			t.Fatal(err)
		}
		if w.State != StateSucceeded || w.Reason != ReasonNormal || w.ExitCode() != 0 || w.Ledger != h.fakes.SpoolPath(testRoot, rec.Job) ||
			len(w.Artifacts) != 1 || w.Artifacts[0].Tool != "create_issue" || *w.Artifacts[0].Kind != "issue" || w.CannotComplete != nil {
			t.Fatalf("wait = %+v", w)
		}
		b, _ := json.Marshal(w)
		for _, key := range []string{`"jid"`, `"job"`, `"room"`, `"state"`, `"reason"`, `"message"`, `"ledger"`, `"artifacts"`, `"cannot_complete":null`} {
			if !strings.Contains(string(b), key) {
				t.Errorf("wait JSON lacks %s: %s", key, b)
			}
		}
	})
	t.Run("cannot_complete", func(t *testing.T) {
		h := newHarness(t)
		pinPrincipals(h, testRoot, "child-1")
		newRun(t, h, "rec-42")
		rec := spawnChild(t, h)
		finishAgent(t, h, rec, StateFailed, `cannot_complete: "no repo matches"`, cannot)
		w, err := WaitChild(context.Background(), h.deps, rec, time.Second, 0)
		if err != nil || w.ExitCode() != ExitFailed || w.Reason != ReasonFailed || w.CannotComplete == nil || w.CannotComplete.Reason != "no repo matches" || len(w.Artifacts) != 0 {
			t.Fatalf("wait = %+v, err = %v", w, err)
		}
	})
	t.Run("killed", func(t *testing.T) {
		h := newHarness(t)
		pinPrincipals(h, testRoot, "child-1")
		newRun(t, h, "rec-42")
		rec := spawnChild(t, h)
		if _, err := ExitWake(context.Background(), h.deps, ExitWakeRequest{Job: rec.Job, Target: rec.Parent, Service: ServiceResult{Result: "signal", ExitStatus: "KILL"}}); err != nil {
			t.Fatal(err)
		}
		w, err := WaitChild(context.Background(), h.deps, rec, time.Second, 0)
		if err != nil || w.ExitCode() != ExitInterrupted || w.Reason != ReasonKilled {
			t.Fatalf("wait = %+v, err = %v", w, err)
		}
	})
	t.Run("aborted", func(t *testing.T) {
		h := newHarness(t)
		pinPrincipals(h, testRoot, "child-1")
		newRun(t, h, "rec-42")
		rec := spawnChild(t, h)
		h.fakes.AppendRecord(t, rec.Parent, rec.Job, StateAborted, "cancelled by a holder", "")
		w, err := WaitChild(context.Background(), h.deps, rec, time.Second, 0)
		if err != nil || w.ExitCode() != ExitAborted || w.Reason != ReasonShutdown {
			t.Fatalf("wait = %+v, err = %v", w, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		h := newHarness(t)
		pinPrincipals(h, testRoot, "child-1")
		newRun(t, h, "rec-42")
		rec := spawnChild(t, h)
		w, err := WaitChild(context.Background(), h.deps, rec, 50*time.Millisecond, 200*time.Millisecond)
		if err != nil || w.State != StateRunning || w.ExitCode() != ExitWaitTimeout {
			t.Fatalf("wait = %+v, err = %v", w, err)
		}
		if got := h.fakes.Cancelled(t); len(got) != 1 || got[0] != rec.Job {
			t.Errorf("a timed-out wait must request cancellation of the job: %v", got)
		}
	})
	t.Run("timeout then the agent aborts within the grace", func(t *testing.T) {
		h := newHarness(t)
		pinPrincipals(h, testRoot, "child-1")
		newRun(t, h, "rec-42")
		rec := spawnChild(t, h)
		go func() {
			for len(h.fakes.Cancelled(t)) == 0 {
				time.Sleep(10 * time.Millisecond)
			}
			h.fakes.AppendRecord(t, rec.Parent, rec.Job, StateAborted, "cancelled by a holder", "")
		}()
		w, err := WaitChild(context.Background(), h.deps, rec, 50*time.Millisecond, 5*time.Second)
		if err != nil || w.State != StateAborted || w.ExitCode() != ExitAborted {
			t.Fatalf("wait = %+v, err = %v", w, err)
		}
		if got := h.fakes.Cancelled(t); len(got) != 1 || got[0] != rec.Job {
			t.Errorf("cancelled = %v", got)
		}
	})
}

func TestResolve(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newRun(t, h, "rec-42")
	note := "note"
	out, err := Resolve(context.Background(), h.deps, ResolveRequest{
		RunJob: res.RunJob, State: StateFailed, Reason: `cannot_complete: "no repo matches"`,
		FallbackArtifacts: []Artifact{{Tool: "orgzly", Kind: &note, URIs: []string{"orgzly://inbox/1"}}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.State != StateFailed || out.Reason != ReasonFailed || out.AlreadyResolved || len(out.Woken) != 1 || out.Woken[0] != testIssuer {
		t.Fatalf("outcome = %+v", out)
	}

	var ledger RunLedger
	data, err := JobLedger(context.Background(), h.deps, res.RunJob, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	calls := callsExcept(ledger, TeardownTool)
	if len(calls) != 1 || calls[0].Tool != "fallback" || calls[0].Ran == nil || !*calls[0].Ran ||
		!calls[0].OK || calls[0].URIs[0] != "orgzly://inbox/1" || ledger.Resolved == nil || ledger.Resolved.State != StateFailed {
		t.Errorf("run ledger = %s", data)
	}

	recs := h.fakes.Records(t, testIssuer, res.RunJob)
	term := lastRecord(t, recs)
	if term.Type != StateFailed || term.Message != `cannot_complete: "no repo matches"` || term.ResultRef != h.fakes.SpoolPath(testIssuer, res.RunJob) {
		t.Errorf("run terminal = %+v", term)
	}
	spool, _ := os.ReadFile(term.ResultRef)
	if !strings.Contains(string(spool), `"fallback"`) {
		t.Errorf("the run job's result spool must carry the run ledger: %s", spool)
	}
	wakes := h.fakes.Wakes(t)
	if len(wakes) != 1 || wakes[0].Target != testIssuer || wakes[0].From != testRoot || wakes[0].Source != ResolveSource ||
		!strings.HasPrefix(wakes[0].Message, "exit "+res.RunJob+" failed reason=failed") {
		t.Errorf("wakes = %+v", wakes)
	}

	again, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "late"})
	if err != nil || !again.AlreadyResolved || again.State != StateFailed {
		t.Errorf("second resolve = %+v, err = %v", again, err)
	}
	if countTerminals(h.fakes.Records(t, testIssuer, res.RunJob)) != 1 || len(h.fakes.Wakes(t)) != 1 {
		t.Error("a second resolve must write and wake nothing")
	}
}

func TestResolve_RejectsBadState(t *testing.T) {
	h := newHarness(t)
	if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: "x-1", State: StateAborted}); err == nil {
		t.Fatal("want an error")
	}
}

func TestJobLedgerAndHandles_ChildJob(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	newRun(t, h, "rec-42")
	rec := spawnChild(t, h)
	if _, err := JobLedger(context.Background(), h.deps, rec.Job, ""); err == nil {
		t.Error("no ledger before the agent wrote one")
	}
	finishAgent(t, h, rec, StateSucceeded, "evaluator passed", jugglerloop.Ledger{Schema: 1, Calls: []jugglerloop.LedgerCall{}})
	data, err := JobLedger(context.Background(), h.deps, rec.Job, "")
	if err != nil || !json.Valid(data) {
		t.Fatalf("ledger = %s, err = %v", data, err)
	}
	holders, err := JobHandles(context.Background(), h.deps, rec.Job, "")
	if err != nil || len(holders) != 1 || holders[0].Principal != testRoot || holders[0].Rights != DefaultRights {
		t.Fatalf("holders = %+v, err = %v", holders, err)
	}
	if _, err := JobHandles(context.Background(), h.deps, "unknown-1", ""); err == nil {
		t.Error("an unknown job without --target must be an error")
	}
}

func TestFillTemplate(t *testing.T) {
	fill := TemplateFill{Principal: "p-9", Parent: "root-1", Room: "r@x", Task: "do it"}
	b, canonical, err := FillTemplate(templateBytes(), fill)
	if err != nil || b.Principal != "p-9" || b.Task != "do it" || !strings.Contains(string(canonical), `principal = "p-9"`) {
		t.Fatalf("brief = %+v, err = %v", b, err)
	}
	if _, _, err := FillTemplate(append(templateBytes(), []byte("bogus = 1\n")...), fill); err == nil {
		t.Error("unknown fields must still be rejected")
	}
	if _, _, err := FillTemplate(templateBytes(`principal = "pinned"`), fill); err == nil {
		t.Error("a template may not pin the principal")
	}
	noTask := fill
	noTask.Task = ""
	if _, _, err := FillTemplate(templateBytes(), noTask); err == nil || !strings.Contains(err.Error(), "task") {
		t.Errorf("a filled brief without a task must fail strict validation: %v", err)
	}
	if b, _, err := FillTemplate(templateBytes(`task = "own"`), noTask); err != nil || b.Task != "own" {
		t.Errorf("the template's own task: %+v %v", b, err)
	}
	noModel := strings.Replace(string(templateBytes()), `model = "test-model"`, "", 1)
	if _, _, err := FillTemplate([]byte(noModel), fill); err == nil {
		t.Error("model must be in the template")
	}
}

func TestSystemdRunArgvAndCommandLine(t *testing.T) {
	argv := SystemdRunArgv("systemd-run", true, TransientUnit{
		Name: "u", RuntimeMax: 90500 * time.Millisecond, ExecStopPost: []string{"/j", "exit-wake", "--job", "a b"},
		Env: map[string]string{"B": "2", "A": "1"}, Exec: []string{"/j", "run"},
	})
	want := []string{
		"systemd-run", "--user", "--unit", "u", "--collect", "--property=Type=exec", "--property=RuntimeMaxSec=91",
		"--property=Delegate=yes", `--property=ExecStopPost=/j exit-wake --job "a b"`, "--setenv=A=1", "--setenv=B=2", "/j", "run",
	}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Errorf("argv = %q", argv)
	}
	if got := SystemdCommandLine([]string{"/x", "100%", "$HOME"}); got != "/x 100%% $$HOME" {
		t.Errorf("command line = %q", got)
	}
}

func TestStripPrincipalEnv(t *testing.T) {
	got := StripPrincipalEnv([]string{"PATH=/bin", "CLOWN_SESSION_ID=k", "TROUPE_XMPP_USER=u", "TROUPE_XMPP_PASSWORD_FILE=/p", "TROUPE_MINT_PASSWORD_FILE=/m", "TROUPE_MINT_USER=troupe-minter", "TROUPE_TRANSPORT=xmpp-native"})
	if strings.Join(got, ",") != "PATH=/bin,TROUPE_TRANSPORT=xmpp-native" {
		t.Errorf("env = %v", got)
	}
}

func TestResolveBinary(t *testing.T) {
	t.Setenv(RingmasterBinEnv, "/env/ringmaster")
	if got := ResolveBinary("/flag/ringmaster", RingmasterBinEnv, "ringmaster"); got != "/flag/ringmaster" {
		t.Errorf("flag must win: %s", got)
	}
	if got := ResolveBinary("", RingmasterBinEnv, "ringmaster"); got != "/env/ringmaster" {
		t.Errorf("env must win over the bare name: %s", got)
	}
	if got := ResolveBinary("", "JUGGLER_UNSET_BIN", "ringmaster"); got != "ringmaster" {
		t.Errorf("bare name: %s", got)
	}
}

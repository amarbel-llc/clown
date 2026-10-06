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
	"code.linenisgreat.com/clown/internal/jugglerrun/jugglerruntest"
)

const (
	testOperator = "operator@xmpp.test"
	testCanary   = "canary@rooms.test"
	rootJID      = testRoot + "@xmpp.test"
)

// newProvisionedRun is newRun with a room the run creates on rooms.test.
func newProvisionedRun(t *testing.T, h *harness, key string) NewRunResult {
	t.Helper()
	res, err := NewRun(context.Background(), h.deps, NewRunRequest{RunKey: key, Issuer: testIssuer, Input: []byte(`{"recording":"r1"}`), RoomDomain: "rooms.test", OperatorJID: testOperator})
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	return res
}

// troupeArgvs is every recorded troupe argv from index from on, joined.
func troupeArgvs(t *testing.T, h *harness, from int) []string {
	t.Helper()
	var out []string
	for _, c := range h.fakes.Calls(t, "troupe")[from:] {
		out = append(out, strings.Join(c.Argv, " "))
	}
	return out
}

func runLedger(t *testing.T, h *harness, job string) RunLedger {
	t.Helper()
	data, err := JobLedger(context.Background(), h.deps, job, "")
	if err != nil {
		t.Fatal(err)
	}
	var l RunLedger
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNewRun_CreatesTheRoomAsTheRootWithTheOperatorAsOwner(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "Rec-42")
	if res.Room != "rec-42@rooms.test" || res.OperatorJID != testOperator {
		t.Fatalf("result = %+v", res)
	}
	var create jugglerruntest.Call
	for _, c := range h.fakes.Calls(t, "troupe") {
		if c.Argv[0] == "muc" && c.Argv[1] == "create" {
			create = c
		}
	}
	if got := strings.Join(create.Argv, " "); got != "muc create --room rec-42@rooms.test --owner "+testOperator {
		t.Errorf("muc create argv = %q", got)
	}
	if create.Env["TROUPE_XMPP_USER"] != testRoot || create.Env["TROUPE_XMPP_PASSWORD_FILE"] != res.RootCredentialRef || create.Env[SessionIDEnv] != testRoot {
		t.Errorf("muc create must run as the run root: %v", create.Env)
	}
	room, ok := h.fakes.Room(t, res.Room)
	if !ok || strings.Join(room.With(AffiliationOwner), ",") != testOperator+","+rootJID {
		t.Errorf("room = %+v", room)
	}
	rec, _ := h.deps.Store.LoadRun("Rec-42")
	if !rec.RoomCreated || rec.OperatorJID != testOperator {
		t.Errorf("run record = %+v", rec)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"operator_jid":"`+testOperator+`"`) {
		t.Errorf("JSON = %s", b)
	}
	if posts := h.fakes.MUC(t); len(posts) != 1 || posts[0].Room != res.Room {
		t.Errorf("the run input must land in the created room: %+v", posts)
	}
}

func TestNewRun_RetryAfterAFailurePastRoomCreationResumesTheSameRoot(t *testing.T) {
	h := newHarness(t) // the real NewRootPrincipal: derived from the run key
	h.fakes.Fail(t, "ringmaster", "start")
	req := NewRunRequest{RunKey: "k", Issuer: testIssuer, Input: []byte("x"), RoomDomain: "rooms.test", OperatorJID: testOperator}
	_, err := NewRun(context.Background(), h.deps, req)
	var pending *PendingRunError
	if !errors.As(err, &pending) || pending.RunKey != "k" || pending.RootPrincipal != NewRootPrincipal("k") || pending.Room != "k@rooms.test" || !strings.Contains(err.Error(), "starting the run job") {
		t.Fatalf("err = %#v", err)
	}
	root := NewRootPrincipal("k")
	if _, ok := h.fakes.Room(t, "k@rooms.test"); !ok {
		t.Error("a created room is persistent and must be left")
	}
	posts := h.fakes.MUC(t)
	if len(posts) != 2 || !strings.Contains(posts[1].Subject, `"type":"withdrawn"`) {
		t.Errorf("the run input must be withdrawn: %+v", posts)
	}
	if len(h.fakes.Revoked(t)) != 0 {
		t.Error("the root is kept for the retry")
	}
	if rec, _ := h.deps.Store.LoadRun("k"); rec == nil || !rec.Pending || !rec.RoomCreated || rec.RootPrincipal != root {
		t.Fatalf("pending record = %+v", rec)
	}

	if err := os.Remove(h.fakes.Dir + "/fail/ringmaster-start"); err != nil {
		t.Fatal(err)
	}
	res, err := NewRun(context.Background(), h.deps, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.Existing || !res.Resumed || res.RootPrincipal != root || res.Room != "k@rooms.test" || res.RunJob == "" {
		t.Fatalf("retry result = %+v", res)
	}
	var creates []string
	for _, c := range h.fakes.Calls(t, "troupe") {
		if c.Argv[0] == "muc" && c.Argv[1] == "create" {
			creates = append(creates, c.Env["TROUPE_XMPP_USER"])
		}
	}
	if len(creates) != 2 || creates[0] != root || creates[1] != root {
		t.Errorf("muc create must run twice, idempotently, as the same root: %v", creates)
	}
	if jobs := h.fakes.Jobs(t, testIssuer); len(jobs) != 1 || jobs[0] != res.RunJob {
		t.Errorf("exactly one run job: %v", jobs)
	}
	rec, _ := h.deps.Store.LoadRun("k")
	if rec.Pending || rec.RunJob != res.RunJob {
		t.Errorf("record = %+v", rec)
	}
	if again, err := NewRun(context.Background(), h.deps, req); err != nil || !again.Existing || again.RunJob != res.RunJob {
		t.Errorf("a complete run is existing: %+v, %v", again, err)
	}
}

func TestNewRun_RetryAfterAFailedInputPostResumes(t *testing.T) {
	h := newHarness(t)
	h.fakes.Fail(t, "troupe", "muc-send")
	req := NewRunRequest{RunKey: "k", Issuer: testIssuer, Input: []byte("x"), RoomDomain: "rooms.test", OperatorJID: testOperator}
	if _, err := NewRun(context.Background(), h.deps, req); err == nil {
		t.Fatal("want an error")
	}
	if err := os.Remove(h.fakes.Dir + "/fail/troupe-muc-send"); err != nil {
		t.Fatal(err)
	}
	res, err := NewRun(context.Background(), h.deps, req)
	if err != nil || res.RootPrincipal != NewRootPrincipal("k") || res.RootStanza == "" {
		t.Fatalf("retry = %+v, %v", res, err)
	}
	if jobs := h.fakes.Jobs(t, testIssuer); len(jobs) != 1 {
		t.Errorf("run jobs = %v", jobs)
	}
}

func TestSpawnChild_AffiliatesTheChildAsAMemberBeforeLaunch(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newProvisionedRun(t, h, "rec-42")
	rec := spawnChild(t, h)

	var order []string
	for _, c := range h.fakes.Calls(t, "") {
		switch {
		case c.Tool == "troupe" && c.Argv[0] == "muc" && c.Argv[1] == "affiliate":
			order = append(order, strings.Join(c.Argv, " "))
			if c.Env["TROUPE_XMPP_USER"] != testRoot {
				t.Errorf("the affiliation must be set as the run root: %v", c.Env)
			}
		case c.Tool == "systemd-run":
			order = append(order, "launch")
		}
	}
	want := []string{"muc affiliate --room " + res.Room + " --affiliation member --jid " + rec.JID, "launch"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("order = %q, want %q", order, want)
	}
	if room, _ := h.fakes.Room(t, res.Room); room.Affiliations[rec.JID] != AffiliationMember {
		t.Errorf("room = %+v", room)
	}
}

func TestSpawnChild_LaunchFailureRemovesTheMembership(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newProvisionedRun(t, h, "rec-42")
	h.fakes.Fail(t, "systemd-run", "launch")
	if _, _, err := SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"}); err == nil {
		t.Fatal("want an error")
	}
	room, _ := h.fakes.Room(t, res.Room)
	if _, ok := room.Affiliations["child-1@xmpp.test"]; ok {
		t.Errorf("the failed child must be de-affiliated: %+v", room)
	}
}

func TestResolve_PostsTheCanaryFirstThenTearsDownEveryAccount(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newProvisionedRun(t, h, "rec-42")
	child := spawnChild(t, h)
	finishAgent(t, h, child, StateSucceeded, "evaluator passed", emptyAgentLedger)
	before := len(h.fakes.Calls(t, "troupe"))

	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "issue filed", ResultLine: "rec-42: issue filed", CanaryRoom: testCanary})
	if err != nil {
		t.Fatal(err)
	}
	if !out.TornDown || len(out.Teardown) != 2 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Canary == nil || out.Canary.Room != testCanary || !out.Canary.Posted || !strings.HasPrefix(out.Canary.Stanza, "chat-") {
		t.Errorf("canary = %+v", out.Canary)
	}
	calls := h.fakes.Calls(t, "troupe")[before:]
	if c := calls[0]; c.Argv[0] != "muc" || c.Argv[1] != "send" || !hasArg(c.Argv, testCanary) || !hasArg(c.Argv, "rec-42: issue filed") ||
		!hasArg(c.Argv, ResolveSource) || c.Env["TROUPE_XMPP_USER"] != testRoot {
		t.Errorf("the canary post must be resolve's first troupe call, as the root: %+v", c)
	}
	childPW, rootPW := child.CredentialRef, res.RootCredentialRef
	want := []string{
		"message --target " + testIssuer, // the exit wake (prefix)
		"muc affiliate --room " + res.Room + " --affiliation none --jid " + child.JID,
		"mint-revoke --session-key child-1 --password-file " + childPW,
		"muc affiliate --room " + res.Room + " --affiliation none --jid " + rootJID,
		"mint-revoke --session-key " + testRoot + " --password-file " + rootPW,
	}
	got := troupeArgvs(t, h, before+1)
	if len(got) != len(want) {
		t.Fatalf("troupe calls after the canary = %q", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, p := range []string{childPW, rootPW} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must be deleted: %v", p, err)
		}
	}
	if room, _ := h.fakes.Room(t, res.Room); strings.Join(room.With(AffiliationOwner), ",") != testOperator || len(room.Affiliations) != 1 {
		t.Errorf("only the operator may remain: %+v", room)
	}

	l := runLedger(t, h, res.RunJob)
	if c := l.Calls[0]; c.Tool != CanaryTool || !c.OK || !strings.HasPrefix(c.StanzaID, "chat-") {
		t.Errorf("first ledger entry = %+v", c)
	}
	td := callsOf(l, TeardownTool)
	if len(td) != 2 || td[0].Principal != "child-1" || td[0].JID != child.JID || !td[0].OK || td[0].Kind != "account" || td[0].Reason != "revoked" ||
		td[1].Principal != testRoot || td[1].JID != rootJID || !td[1].OK || td[1].Reason != "revoked" {
		t.Errorf("teardown entries = %+v", td)
	}
	spool, _ := os.ReadFile(h.fakes.SpoolPath(testIssuer, res.RunJob))
	if !strings.Contains(string(spool), `"teardown"`) {
		t.Errorf("the spool must carry the teardown entries: %s", spool)
	}
	if again := newProvisionedRun(t, h, "rec-42"); !again.Existing || !again.Resolved || !again.TornDown {
		t.Errorf("repeated new-run = %+v", again)
	}

	// Idempotent: a second resolve touches no account and posts nothing.
	n := len(h.fakes.Calls(t, ""))
	again, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "late", ResultLine: "x", CanaryRoom: testCanary})
	if err != nil || !again.AlreadyResolved || !again.TornDown || len(again.Teardown) != 0 {
		t.Errorf("second resolve = %+v, %v", again, err)
	}
	if m := len(h.fakes.Calls(t, "")); m != n {
		t.Errorf("a torn-down run must make no platform call: %d became %d", n, m)
	}
}

var emptyAgentLedger = jugglerloop.Ledger{Schema: 1, Calls: []jugglerloop.LedgerCall{}}

func TestResolve_KeepsARunningChildsAccountAndTheRoot(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newProvisionedRun(t, h, "rec-42")
	child := spawnChild(t, h)

	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateFailed, Reason: "x", StopGrace: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if out.TornDown || len(out.Teardown) != 2 || out.Teardown[0].OK || out.Teardown[0].Reason != "still running" ||
		out.Teardown[1].OK || out.Teardown[1].Principal != testRoot {
		t.Fatalf("outcome = %+v", out)
	}
	if got := h.fakes.Revoked(t); len(got) != 0 {
		t.Errorf("no account may be revoked while a child runs: %v", got)
	}
	for _, p := range []string{child.CredentialRef, res.RootCredentialRef} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must be kept: %v", p, err)
		}
	}

	// Once the child is terminal, a re-run does only the teardown.
	h.fakes.AppendRecord(t, child.Parent, child.Job, StateInterrupted, "crash: late", "")
	wakes := len(h.fakes.Wakes(t))
	again, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateFailed, Reason: "x"})
	if err != nil || !again.AlreadyResolved || !again.TornDown || len(again.Teardown) != 2 {
		t.Fatalf("re-run = %+v, %v", again, err)
	}
	if len(h.fakes.Wakes(t)) != wakes || countTerminals(h.fakes.Records(t, testIssuer, res.RunJob)) != 1 {
		t.Error("the re-run must not re-resolve")
	}
	if got := h.fakes.Revoked(t); len(got) != 2 {
		t.Errorf("revoked = %v", got)
	}
}

func TestResolve_PreCreatedRoomKeepsTheRootAffiliation(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot, "child-1")
	res := newRun(t, h, "rec-42")
	child := spawnChild(t, h)
	finishAgent(t, h, child, StateSucceeded, "evaluator passed", emptyAgentLedger)

	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"})
	if err != nil || !out.TornDown {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	for _, c := range h.fakes.Calls(t, "troupe") {
		if c.Argv[0] == "muc" && c.Argv[1] != "send" {
			t.Errorf("a pre-created room is not administered: %q", c.Argv)
		}
	}
	root := out.Teardown[len(out.Teardown)-1]
	if root.Principal != testRoot || !root.OK || !strings.Contains(root.Reason, "root affiliation kept: the room was not provisioned by juggler") {
		t.Errorf("root entry = %+v", root)
	}
}

func TestResolve_RootWithoutAnOperatorOwnerKeepsItsAffiliation(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "rec-42")
	rec, _ := h.deps.Store.LoadRun("rec-42")
	rec.OperatorJID = "" // a record from before --operator-jid was required
	if err := h.deps.Store.SaveRun(rec); err != nil {
		t.Fatal(err)
	}
	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"})
	if err != nil || !out.TornDown || len(out.Teardown) != 1 || !strings.Contains(out.Teardown[0].Reason, "no operator owner") {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if room, _ := h.fakes.Room(t, res.Room); room.Affiliations[rootJID] != AffiliationOwner {
		t.Errorf("the root must not try to step down: %+v", room)
	}
}

func TestResolve_KeepAccounts(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "rec-42")
	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done", KeepAccounts: true})
	if err != nil || out.TornDown || len(out.Teardown) != 0 {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if _, err := os.Stat(res.RootCredentialRef); err != nil || len(h.fakes.Revoked(t)) != 0 {
		t.Errorf("--keep-accounts must keep the root: %v", err)
	}
	if again := newProvisionedRun(t, h, "rec-42"); again.TornDown {
		t.Errorf("repeated new-run = %+v", again)
	}
	if out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"}); err != nil || !out.TornDown {
		t.Errorf("a later resolve finishes the teardown: %+v, %v", out, err)
	}
}

func TestResolve_LoginFailureAtRevokeMeansAlreadyGone(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "rec-42")
	h.fakes.Fail(t, "troupe", "mint-revoke-login")
	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"})
	if err != nil || !out.TornDown || out.Teardown[0].Reason != "already gone: login failed" {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if _, err := os.Stat(res.RootCredentialRef); !os.IsNotExist(err) {
		t.Errorf("resolve deletes the file itself: %v", err)
	}
}

func TestIsLoginFailure(t *testing.T) {
	for stderr, want := range map[string]bool{
		"troupe mint-revoke: mint: xmpp: negotiate: Unable to authorize you with the authentication credentials you've sent.": true,
		"troupe mint-revoke: mint: xmpp: negotiate: not-authorized":                                                           true,
		"troupe mint-revoke: xmpp: negotiate: sasl: not-authorized":                                                           true,
		"troupe mint-revoke: xmpp: negotiate: tls: failed to verify certificate":                                              false,
		"troupe mint-revoke: xmpp: negotiate: EOF":                                                                            false,
		"troupe mint-revoke: xmpp: dial 127.0.0.1:5222: connection refused":                                                   false,
	} {
		if got := IsLoginFailure(&CommandError{Argv: []string{"troupe", "mint-revoke"}, ExitCode: 1, Stderr: stderr, Err: errors.New("exit status 1")}); got != want {
			t.Errorf("IsLoginFailure(%q) = %v, want %v", stderr, got, want)
		}
	}
	if IsLoginFailure(&CommandError{Argv: []string{"troupe"}, ExitCode: 2, Stderr: "xmpp: negotiate: not-authorized"}) {
		t.Error("only exit 1 is a login failure")
	}
}

func TestResolve_NegotiationFailureAtRevokeKeepsTheAccount(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "rec-42")
	h.fakes.Fail(t, "troupe", "mint-revoke-tls")
	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"})
	if err != nil || out.TornDown || len(out.Teardown) != 1 || out.Teardown[0].OK || !strings.Contains(out.Teardown[0].Reason, "tls: failed to verify certificate") {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if _, err := os.Stat(res.RootCredentialRef); err != nil {
		t.Errorf("a TLS failure must not cost the credential: %v", err)
	}
}

func TestResolve_RetriedPartialResolvePostsTheCanaryOnce(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "rec-42")
	h.fakes.Fail(t, "troupe", "message") // the exit wake
	req := ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done", ResultLine: "rec-42: done", CanaryRoom: testCanary}
	first, err := Resolve(context.Background(), h.deps, req)
	if err == nil {
		t.Fatal("want the exit-wake failure")
	}
	if first.Canary == nil || !first.Canary.Posted || first.Canary.Stanza == "" {
		t.Fatalf("first canary = %+v", first.Canary)
	}
	if err := os.Remove(h.fakes.Dir + "/fail/troupe-message"); err != nil {
		t.Fatal(err)
	}
	out, err := Resolve(context.Background(), h.deps, req)
	if err != nil || out.AlreadyResolved || !out.TornDown || len(out.Woken) != 1 {
		t.Fatalf("retry = %+v, %v", out, err)
	}
	if out.Canary == nil || !out.Canary.Posted || out.Canary.Stanza != first.Canary.Stanza {
		t.Errorf("retry canary = %+v, want the first attempt's %s", out.Canary, first.Canary.Stanza)
	}
	if again, err := Resolve(context.Background(), h.deps, req); err != nil || !again.AlreadyResolved || again.Canary == nil || again.Canary.Stanza != first.Canary.Stanza {
		t.Errorf("resolved re-run canary = %+v, %v", again.Canary, err)
	}
	var posts int
	for _, p := range h.fakes.MUC(t) {
		if p.Room == testCanary {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("canary posts = %d, want 1", posts)
	}
	if n := len(callsOf(runLedger(t, h, res.RunJob), CanaryTool)); n != 1 {
		t.Errorf("canary ledger entries = %d, want 1", n)
	}
}

func TestSpawnChild_RacingResolveIsRefusedOrTornDown(t *testing.T) {
	for i := 0; i < 5; i++ {
		h := newHarness(t)
		pinPrincipals(h, testRoot, "child-1")
		res := newProvisionedRun(t, h, "rec-42")
		var (
			child   *ChildRecord
			spawnEr error
			done    = make(chan struct{})
		)
		go func() {
			defer close(done)
			child, _, spawnEr = SpawnChild(context.Background(), h.deps, SpawnRequest{Brief: templateBytes(), Task: []byte(testTask), RunKey: "rec-42", JugglerBin: "/bin/juggler"})
		}()
		if _, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateFailed, Reason: "x", StopGrace: 50 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		<-done
		run, _ := h.deps.Store.LoadRun("rec-42")
		switch {
		case spawnEr != nil:
			if !strings.Contains(spawnEr.Error(), "already resolved") {
				t.Fatalf("spawn error = %v", spawnEr)
			}
		case run.TornDown:
			if _, err := os.Stat(child.CredentialRef); !os.IsNotExist(err) {
				t.Fatalf("run torn down with a live child account: %v", err)
			}
		default:
			// The child won the lock and is still running: resolve kept its
			// account (and the root's) rather than tearing down around it.
			if _, err := os.Stat(child.CredentialRef); err != nil {
				t.Fatalf("a kept child must keep its file: %v", err)
			}
		}
	}
}

func TestResolve_RevokeFailureKeepsTheAccount(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "rec-42")
	h.fakes.Fail(t, "troupe", "mint-revoke")
	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done"})
	if err != nil || out.TornDown || out.Teardown[0].OK || !strings.HasPrefix(out.Teardown[0].Reason, "kept: mint-revoke failed") {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if _, err := os.Stat(res.RootCredentialRef); err != nil {
		t.Errorf("an unreachable server must not cost the credential: %v", err)
	}
}

func TestResolve_CanaryFailureDoesNotChangeTheOutcome(t *testing.T) {
	h := newHarness(t)
	pinPrincipals(h, testRoot)
	res := newProvisionedRun(t, h, "rec-42")
	h.fakes.Fail(t, "troupe", "muc-send")
	out, err := Resolve(context.Background(), h.deps, ResolveRequest{RunJob: res.RunJob, State: StateSucceeded, Reason: "done", ResultLine: "ok", CanaryRoom: testCanary})
	if err != nil || out.State != StateSucceeded || !out.TornDown {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if out.Canary == nil || out.Canary.Posted || out.Canary.Stanza != "" {
		t.Errorf("canary = %+v", out.Canary)
	}
	if c := runLedger(t, h, res.RunJob).Calls[0]; c.Tool != CanaryTool || c.OK || !strings.Contains(c.Reason, "posting to "+testCanary+" failed") {
		t.Errorf("canary note = %+v", c)
	}
}

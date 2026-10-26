package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestLedger(t *testing.T, max int, clock *time.Time) *callLedger {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, err := moxinLedgerPath("sess-1", "issue-filer")
	if err != nil {
		t.Fatal(err)
	}
	return &callLedger{path: path, max: max, window: time.Hour, now: func() time.Time { return *clock }}
}

func TestLedgerCapsAndSlides(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	l := newTestLedger(t, 3, &now)
	for i := 0; i < 3; i++ {
		ok, _, err := l.take()
		if err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
		now = now.Add(10 * time.Minute)
	}
	ok, retryAt, err := l.take()
	if err != nil || ok {
		t.Fatalf("4th call: ok=%v err=%v, want refused", ok, err)
	}
	if want := time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC); !retryAt.Equal(want) {
		t.Errorf("retry at %v, want %v (the oldest call leaves the window)", retryAt, want)
	}
	// 31 minutes later the first call (12:00) is out of the window: one slot.
	now = time.Date(2026, 10, 26, 13, 1, 0, 0, time.UTC)
	if ok, _, err := l.take(); err != nil || !ok {
		t.Errorf("after the window slid: ok=%v err=%v", ok, err)
	}
	if ok, _, _ := l.take(); ok {
		t.Error("only one slot should have opened")
	}
	fi, err := os.Stat(l.path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("ledger file: %v %v, want 0600", fi, err)
	}
}

// The count belongs to the session and the moxin, in a file: a second ledger
// (a relaunched frontend) sees it.
func TestLedgerSurvivesARestartAndIsPerSessionAndMoxin(t *testing.T) {
	now := time.Now()
	l := newTestLedger(t, 1, &now)
	if ok, _, _ := l.take(); !ok {
		t.Fatal("first call refused")
	}
	again := &callLedger{path: l.path, max: 1, window: time.Hour, now: l.now}
	if ok, _, _ := again.take(); ok {
		t.Error("a new process did not see the earlier call")
	}
	otherSession, _ := moxinLedgerPath("sess-2", "issue-filer")
	otherMoxin, _ := moxinLedgerPath("sess-1", "other")
	if otherSession == l.path || otherMoxin == l.path {
		t.Error("the ledger path must differ per session and per moxin")
	}
	if strings.Contains(l.path, "sess-1") {
		t.Error("the session key is hashed in the file name")
	}
}

func TestLedgerNeverExceedsTheCapUnderConcurrency(t *testing.T) {
	now := time.Now()
	l := newTestLedger(t, 5, &now)
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _, err := l.take(); err == nil && ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 5 {
		t.Fatalf("granted %d of 24 concurrent calls, want exactly 5", granted.Load())
	}
}

func TestLedgerFailsClosed(t *testing.T) {
	now := time.Now()
	l := newTestLedger(t, 5, &now)
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := l.take(); ok || err == nil {
		t.Errorf("a corrupt counter: ok=%v err=%v, want an error and no grant", ok, err)
	}
	// A path whose directory cannot be made.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := &callLedger{path: filepath.Join(blocker, "x", "l.json"), max: 5, window: time.Hour, now: time.Now}
	if ok, _, err := bad.take(); ok || err == nil {
		t.Errorf("an unusable state dir: ok=%v err=%v", ok, err)
	}
}

const fakeMoxinServer = `#!/bin/sh
while IFS= read -r l; do
  case $l in *'"id":'*) ;; *) continue ;; esac
  id=${l#*'"id":'}
  id=${id%%[,\}]*}
  printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"child"}]}}\n' "$id"
done
`

// A real child process behind the proxy: everything but a tools/call past the
// cap reaches it and is answered by it; the refused call is answered by the
// proxy, as an isError result, and never reaches the child.
func TestProxyRefusesCallsPastTheCapAndForwardsTheRest(t *testing.T) {
	now := time.Now()
	ledger := newTestLedger(t, 2, &now)
	script := filepath.Join(t.TempDir(), "server.sh")
	if err := os.WriteFile(script, []byte(fakeMoxinServer), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	childIn, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var calls, refusals atomic.Int32
	var refusalText atomic.Value
	p := &capProxy{
		name: "issue-filer", ledger: ledger, out: &out,
		onCall:   func(string) { calls.Add(1) },
		onRefuse: func(text string) { refusals.Add(1); refusalText.Store(text) },
	}
	client := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"file"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"file"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"file"}}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"file"}}`, // a notification: refused, no answer
		`{"jsonrpc":"2.0","id":"str-6","method":"tools/call","params":{"name":"file"}}`,
		`[{"jsonrpc":"2.0","id":7,"method":"tools/call"}]`, // a batch could carry a call past the count
		"not json at all",
	}, "\n") + "\n"
	if err := p.serve(strings.NewReader(client), childIn, childOut); err != nil {
		t.Fatalf("serve: %v", err)
	}
	_ = cmd.Wait()

	var gotIDs []string
	answers := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("output line %q is not JSON: %v", line, err)
		}
		id := string(m.ID)
		gotIDs = append(gotIDs, id)
		switch {
		case m.Error != nil:
			answers[id] = "error"
		case m.Result.IsError:
			answers[id] = "refused: " + m.Result.Content[0].Text
		default:
			answers[id] = "child"
		}
	}
	for _, id := range []string{"1", "2", "3", "4"} {
		if answers[id] != "child" {
			t.Errorf("id %s answered %q, want the child's answer", id, answers[id])
		}
	}
	for _, id := range []string{"5", `"str-6"`} {
		if !strings.HasPrefix(answers[id], "refused: issue-filer: call limit reached (2 per 1h0m0s for this session)") {
			t.Errorf("id %s answered %q, want the cap refusal", id, answers[id])
		}
	}
	if !strings.Contains(answers["5"], "Nothing was sent") {
		t.Errorf("the refusal must say nothing was sent: %q", answers["5"])
	}
	if answers["null"] != "error" {
		t.Errorf("the batch: %q, want a JSON-RPC error", answers["null"])
	}
	if len(gotIDs) != 7 {
		t.Errorf("answers for %v: want 1,2,3,4 from the child, 5 and \"str-6\" refused, the batch error, and none for the notification or the non-JSON line the child skipped", gotIDs)
	}
	if calls.Load() != 2 || refusals.Load() != 3 {
		t.Errorf("counted %d calls and %d refusals, want 2 and 3 (the notification counts as refused)", calls.Load(), refusals.Load())
	}
}

// A counter that cannot be kept refuses the call and says why.
func TestProxyRefusesWhenTheCounterIsBroken(t *testing.T) {
	now := time.Now()
	ledger := newTestLedger(t, 5, &now)
	if err := os.MkdirAll(filepath.Dir(ledger.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger.path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var refused atomic.Value
	p := &capProxy{name: "issue-filer", ledger: ledger, out: &out, onRefuse: func(s string) { refused.Store(s) }}
	var toServer bytes.Buffer
	if err := p.handle([]byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call"}`+"\n"), &toServer); err != nil {
		t.Fatal(err)
	}
	if toServer.Len() != 0 {
		t.Error("the call reached the server with a broken counter")
	}
	if !strings.Contains(out.String(), "cannot be kept") || !strings.Contains(out.String(), `"isError":true`) {
		t.Errorf("answer %q", out.String())
	}
}

func TestMoxinCapMetricNames(t *testing.T) {
	if got := metricMoxinCalls("issue-filer"); got != "clown.moxin.issue-filer.calls" {
		t.Errorf("calls metric %q", got)
	}
	if got := metricMoxinRefused("weird name.x"); got != "clown.moxin.weird_name_x.refused" {
		t.Errorf("refused metric %q (statsd-unsafe characters must not pass)", got)
	}
}

func TestFrontendMoxinCapVerbRefusesBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no arguments":       nil,
		"no command":         {"--name", "m", "--session", "s", "--"},
		"zero cap":           {"--name", "m", "--session", "s", "--max-calls", "0", "--", "x", "y"},
		"bad window":         {"--name", "m", "--session", "s", "--window", "soon", "--", "x", "y"},
		"unknown flag":       {"--frobnicate", "1", "--"},
		"missing flag value": {"--name"},
	} {
		if code := runFrontendMoxinCap(args); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}

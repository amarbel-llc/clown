package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
	"code.linenisgreat.com/clown/internal/0/staging"
	"code.linenisgreat.com/clown/internal/alfa/pluginhost"
)

const testSession = "758c1a41-0000-0000-0000-00fee3fec901"

func writeTestRecord(t *testing.T, root, session, plugin, server string) disconnectlog.Record {
	t.Helper()
	rec, err := disconnectlog.Write(root, disconnectlog.Record{
		Session: session, Plugin: plugin, Server: server,
		Kind: disconnectlog.KindChildExit, ClosedBy: disconnectlog.ClosedByChild,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// rpc runs requests through serveDisconnects and returns the decoded replies.
func rpc(t *testing.T, root, session string, requests ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := serveDisconnects(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out, root, session); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad reply %q: %v", line, err)
		}
		replies = append(replies, m)
	}
	return replies
}

func TestDisconnectsServerServesOnlyItsOwnSessionsRecords(t *testing.T) {
	root := t.TempDir()
	mine := writeTestRecord(t, root, testSession, "cutting-garden", "cg")
	other := writeTestRecord(t, root, "someone-else", "cutting-garden", "cg")

	read := func(id int, uri string) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "resources/read", "params": map[string]any{"uri": uri}})
		return string(b)
	}
	replies := rpc(t, root, testSession,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/templates/list"}`,
		read(4, mine.URI),
		read(5, other.URI),
		read(6, disconnectlog.AllURI(testSession)),
		read(7, "clown://session/"+testSession+"/plugins/../../x"),
		`{"jsonrpc":"2.0","id":8,"method":"tools/list"}`,
	)
	if len(replies) != 8 { // the notification gets no reply
		t.Fatalf("got %d replies, want 8: %v", len(replies), replies)
	}
	caps := replies[0]["result"].(map[string]any)["capabilities"].(map[string]any)
	if _, ok := caps["resources"]; !ok || len(caps) != 1 {
		t.Errorf("capabilities = %v, want resources only", caps)
	}
	listed := replies[1]["result"].(map[string]any)["resources"].([]any)
	found := false
	for _, r := range listed {
		if r.(map[string]any)["uri"] == mine.URI {
			found = true
		}
		if strings.Contains(r.(map[string]any)["uri"].(string), "someone-else") {
			t.Errorf("another session's record is listed: %v", r)
		}
	}
	if !found {
		t.Errorf("own record %s not listed: %v", mine.URI, listed)
	}
	if len(replies[2]["result"].(map[string]any)["resourceTemplates"].([]any)) != 3 {
		t.Errorf("templates = %v", replies[2])
	}
	contents := replies[3]["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)
	if !strings.Contains(contents["text"].(string), `"kind":"child_exit"`) {
		t.Errorf("record text = %v", contents["text"])
	}
	for _, i := range []int{4, 6} { // another principal's record, and a traversal URI
		e := replies[i]["error"].(map[string]any)
		if int(e["code"].(float64)) != rpcResourceNotFound {
			t.Errorf("reply %d error = %v, want resource not found", i, e)
		}
	}
	if !strings.Contains(replies[5]["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)["text"].(string), mine.URI) {
		t.Errorf("the aggregate does not list the record: %v", replies[5])
	}
	if int(replies[7]["error"].(map[string]any)["code"].(float64)) != rpcMethodNotFound {
		t.Errorf("tools/list = %v, want method not found", replies[7])
	}
}

func TestSynthDiagnosticsPluginDirDeclaresOneFrontendServer(t *testing.T) {
	root, err := staging.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dir, err := synthDiagnosticsPluginDir(root, "/bin/clown", testSession, "/state/disconnects")
	if err != nil {
		t.Fatal(err)
	}
	name, err := pluginhost.PluginName(dir)
	if err != nil || name != pluginhost.DiagnosticsPluginName {
		t.Fatalf("plugin name = %q, %v", name, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "clown.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Stdio map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Scope   string   `json:"scope"`
		} `json:"stdioServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	s, ok := cfg.Stdio[disconnectsServerName]
	if !ok || len(cfg.Stdio) != 1 {
		t.Fatalf("stdioServers = %v", cfg.Stdio)
	}
	if s.Command != "/bin/clown" || s.Scope != string(pluginhost.ScopeFrontend) ||
		strings.Join(s.Args, " ") != "disconnects-serve --session "+testSession+" --root /state/disconnects" {
		t.Errorf("server = %+v", s)
	}
}

func TestWriteBridgeExitSkipsADeathTheBridgeAlreadyRecorded(t *testing.T) {
	root := t.TempDir()
	start := time.Now().Add(-time.Minute)
	ev := pluginhost.ExitEvent{PluginName: "cg", ServerName: "s", PID: 7, StartedAt: start, Code: 1, Bridge: true}

	rec, wrote, err := writeServerExit(root, testSession, "/log", ev)
	if err != nil || !wrote || rec.Kind != disconnectlog.KindBridgeExit {
		t.Fatalf("first write = %+v, %v, %v", rec, wrote, err)
	}
	if _, wrote, _ := writeServerExit(root, testSession, "/log", ev); wrote {
		t.Error("a second record was written for the same life")
	}
}

// A started server that is not a bridge has no one else to record its death:
// it always gets a server_exit record carrying the stderr tail, and the tail is
// readable as the server's log resource (clown#241).
func TestNonBridgeServerDeathIsRecordedAndItsLogIsAResource(t *testing.T) {
	root := t.TempDir()
	ev := pluginhost.ExitEvent{
		PluginName: "native", ServerName: "n", PID: 9, StartedAt: time.Now().Add(-time.Minute), Code: 3,
		StderrTail: []disconnectlog.StderrLine{{At: "t1", Line: "boom: out of cheese"}, {At: "t2", Line: "fatal: giving up"}},
	}
	rec, wrote, err := writeServerExit(root, testSession, "/log", ev)
	if err != nil || !wrote || rec.Kind != disconnectlog.KindServerExit || rec.ChildExit == nil || rec.ChildExit.Code != 3 {
		t.Fatalf("write = %+v, %v, %v", rec, wrote, err)
	}
	// Not skipped the way a bridge that recorded itself is.
	if _, wrote, _ := writeServerExit(root, testSession, "/log", ev); !wrote {
		t.Error("a second death of a non-bridge server was not recorded")
	}

	logURI := disconnectlog.LogURI(testSession, "native", "n")
	read := func(id int, uri string) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "resources/read", "params": map[string]any{"uri": uri}})
		return string(b)
	}
	replies := rpc(t, root, testSession,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		read(2, logURI),
		read(3, disconnectlog.LogURI("someone-else", "native", "n")),
		read(4, disconnectlog.LogURI(testSession, "native", "nothing")),
	)
	listed, _ := json.Marshal(replies[0]["result"])
	if !strings.Contains(string(listed), logURI) {
		t.Errorf("the log resource is not listed: %s", listed)
	}
	c := replies[1]["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)
	if c["mimeType"] != "text/plain" || !strings.Contains(c["text"].(string), "fatal: giving up") || !strings.Contains(c["text"].(string), "boom: out of cheese") {
		t.Errorf("log contents = %v", c)
	}
	for _, i := range []int{2, 3} { // another principal's log, a server with no records
		if int(replies[i]["error"].(map[string]any)["code"].(float64)) != rpcResourceNotFound {
			t.Errorf("reply %d = %v, want resource not found", i, replies[i])
		}
	}
	if msg := disconnectWakeMessage(disconnectlog.Entry{Plugin: "native", Server: "n", Kind: rec.Kind, At: rec.At, URI: rec.URI}); !strings.Contains(msg, logURI) {
		t.Errorf("the wake does not name the log resource %s: %s", logURI, msg)
	}
}

func TestWatchDisconnectWakesWakesOnceForNewRecordsOnly(t *testing.T) {
	root := t.TempDir()
	old := writeTestRecord(t, root, testSession, "cg", "s")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var msgs []string
	got := make(chan struct{}, 4)
	send := func(principal, message string) error {
		mu.Lock()
		msgs = append(msgs, principal+": "+message)
		mu.Unlock()
		got <- struct{}{}
		return nil
	}
	watchDisconnectWakes(ctx, root, testSession, disconnectBaseline(root, testSession), 10*time.Millisecond, send, t.Logf)
	fresh := writeTestRecord(t, root, testSession, "cg", "s")

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no wake for the new record")
	}
	select {
	case <-got:
		t.Error("a second wake was sent")
	case <-time.After(100 * time.Millisecond):
	}
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) != 1 || !strings.Contains(msgs[0], fresh.URI) || strings.Contains(msgs[0], old.URI+" ") {
		t.Errorf("wakes = %v", msgs)
	}
}

// A record written after the baseline but before the watcher's first poll (a
// bridge that dies while the servers are still starting) is news, not history.
func TestWatchDisconnectWakesWakesForARecordWrittenBeforeTheWatchStarted(t *testing.T) {
	root := t.TempDir()
	writeTestRecord(t, root, testSession, "cg", "s") // history
	baseline := disconnectBaseline(root, testSession)
	early := writeTestRecord(t, root, testSession, "cg", "s") // during StartAll

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 4)
	send := func(_, message string) error { got <- message; return nil }
	watchDisconnectWakes(ctx, root, testSession, baseline, 10*time.Millisecond, send, t.Logf)

	select {
	case msg := <-got:
		if !strings.Contains(msg, early.URI) {
			t.Errorf("wake %q does not name the early record %s", msg, early.URI)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wake for a record written between the baseline and the watch")
	}
}

func TestRunDisconnectsServeRejectsBadArguments(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{{}, {"--session", "x"}, {"--session", "x", "--root", "relative"}, {"--bogus", "1"}} {
		if code := runDisconnectsServe(args, strings.NewReader(""), &out, &errb); code != 2 {
			t.Errorf("args %v: code %d, want 2", args, code)
		}
	}
}

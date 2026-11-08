package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
)

const recorderTestSession = "9eff073f-a924-48c2-b076-8b02d38f1938"

func recorderEnv(root string) func(string) string {
	env := map[string]string{
		envSession:   recorderTestSession,
		envPlugin:    "cutting-garden",
		envServer:    "cg",
		envRecordDir: root,
		envHostLog:   "/tmp/clown-plugin-host-test.log",
	}
	return func(k string) string { return env[k] }
}

// waitPending blocks until the translator has n requests a handler waits on.
func waitPending(t *testing.T, tr *translator, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for tr.pendingActiveCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("translator never had %d pending request(s)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// clown#344: a disconnect writes one record and answers every waiting request
// with an error that ends in the record's URI, instead of leaving the call to
// fail as a closed connection.
func TestRecorderWritesTheRecordAndFailsWhatIsInFlight(t *testing.T) {
	root := t.TempDir()
	pr, pw := io.Pipe()
	defer pw.Close()
	tr := newTranslator(io.Discard, pr, nullLogger{})
	tail := disconnectlog.NewRing()
	tail.Add("fatal: out of memory TOKEN=hunter2")
	rec := newRecorder(recorderEnv(root), 4242, tr, tail, nullLogger{})
	if rec == nil {
		t.Fatal("newRecorder returned nil with a full identity")
	}

	type result struct {
		resp json.RawMessage
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := tr.SendRequest(context.Background(), `7`, []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"mutate_nodes","arguments":{"secret":"do-not-record"}}}`))
		got <- result{resp, err}
	}()
	waitPending(t, tr, 1)

	summary := rec.record(disconnectlog.KindChildExit, disconnectlog.ClosedByChild, &disconnectlog.Exit{Code: 2})
	if !strings.Contains(summary, "cutting-garden/cg") || !strings.Contains(summary, "child_exit") || !strings.Contains(summary, "/disconnects/1") {
		t.Fatalf("summary = %q", summary)
	}

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("the waiting request must get an error response, not fail: %v", r.err)
		}
		var env struct {
			ID    json.RawMessage `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(r.resp, &env); err != nil || string(env.ID) != "7" || env.Error.Code == 0 {
			t.Fatalf("response %s (%v); want a JSON-RPC error for id 7", r.resp, err)
		}
		if !strings.HasSuffix(env.Error.Message, "(MCP server clown-builtin-diagnostics/disconnects)") || !strings.Contains(env.Error.Message, "clown://session/"+recorderTestSession+"/plugins/cutting-garden/cg/disconnects/1") {
			t.Errorf("error message %q does not name the record", env.Error.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request was never answered")
	}

	raw, err := disconnectlog.Read(root, recorderTestSession, "cutting-garden", "cg", 1)
	if err != nil {
		t.Fatal(err)
	}
	var back disconnectlog.Record
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Kind != disconnectlog.KindChildExit || back.ClosedBy != disconnectlog.ClosedByChild || back.ChildPID != 4242 || back.BridgePID != os.Getpid() ||
		back.ChildExit == nil || back.ChildExit.Code != 2 || back.HostLog != "/tmp/clown-plugin-host-test.log" {
		t.Errorf("record = %+v", back)
	}
	if len(back.InFlight) != 1 || back.InFlight[0].ID != "7" || back.InFlight[0].Method != "tools/call" {
		t.Errorf("in_flight = %+v; want the one tools/call with its id", back.InFlight)
	}
	if len(back.StderrTail) != 1 || strings.Contains(back.StderrTail[0].Line, "hunter2") {
		t.Errorf("stderr_tail = %+v; want one scrubbed line", back.StderrTail)
	}
	if strings.Contains(string(raw), "do-not-record") || strings.Contains(string(raw), "mutate_nodes") {
		t.Errorf("the record holds request content: %s", raw)
	}

	// Once: a second event writes nothing and fails nothing.
	if again := rec.record(disconnectlog.KindChildStdoutEOF, disconnectlog.ClosedByChild, nil); again != "" {
		t.Errorf("a second record() returned %q; want nothing", again)
	}
	if n := len(disconnectlog.List(root, recorderTestSession, "cutting-garden", "cg")); n != 1 {
		t.Errorf("%d records written, want 1", n)
	}
}

// A child's last stderr lines are still in the pipe when its exit is seen; the
// record waits for the tee to reach EOF, so they land in stderr_tail.
func TestRecorderWaitsForTheStderrTeeBeforeSnapshotting(t *testing.T) {
	root := t.TempDir()
	tail := disconnectlog.NewRing()
	tr := newTranslator(io.Discard, strings.NewReader(""), nullLogger{})
	rec := newRecorder(recorderEnv(root), 4242, tr, tail, nullLogger{})

	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		teeStderr(pr, io.Discard, tail)
		close(done)
	}()
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = pw.Write([]byte("fatal: the last words\n"))
		_ = pw.Close()
	}()
	rec.awaitStderr(done, 10*time.Second)

	rec.record(disconnectlog.KindChildExit, disconnectlog.ClosedByChild, &disconnectlog.Exit{Code: 1})
	raw, err := disconnectlog.Read(root, recorderTestSession, "cutting-garden", "cg", 1)
	if err != nil {
		t.Fatal(err)
	}
	var back disconnectlog.Record
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.StderrTail) != 1 || !strings.Contains(back.StderrTail[0].Line, "the last words") {
		t.Errorf("stderr_tail = %+v; want the child's last line", back.StderrTail)
	}
}

// A stderr pipe that never reaches EOF (a grandchild kept the write end) holds
// the record up for the bound and no longer.
func TestRecorderStopsWaitingForStderrAtTheBound(t *testing.T) {
	tr := newTranslator(io.Discard, strings.NewReader(""), nullLogger{})
	rec := newRecorder(recorderEnv(t.TempDir()), 4242, tr, disconnectlog.NewRing(), nullLogger{})
	rec.awaitStderr(make(chan struct{}), 30*time.Millisecond)

	start := time.Now()
	if rec.record(disconnectlog.KindSignal, disconnectlog.ClosedByExternal, nil) == "" {
		t.Fatal("no record written")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("record waited %v for a stderr that never ends", time.Since(start))
	}
}

func TestRecorderIsAbsentWithoutAFullIdentity(t *testing.T) {
	root := t.TempDir()
	tr := newTranslator(io.Discard, strings.NewReader(""), nullLogger{})
	full := recorderEnv(root)
	for _, missing := range []string{envSession, envPlugin, envServer, envRecordDir} {
		getenv := func(k string) string {
			if k == missing {
				return ""
			}
			return full(k)
		}
		if rec := newRecorder(getenv, 1, tr, nil, nullLogger{}); rec != nil {
			t.Errorf("recorder built without %s", missing)
		}
	}
	var none *recorder
	if got := none.record(disconnectlog.KindSignal, disconnectlog.ClosedByExternal, nil); got != "" {
		t.Errorf("a nil recorder returned %q", got)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("a nil recorder wrote %v", entries)
	}
}

// A record that cannot be written still fails the waiting requests, with a
// message that says so, rather than leaving them to hang.
func TestRecorderStillFailsRequestsWhenTheRecordCannotBeWritten(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	defer pw.Close()
	tr := newTranslator(io.Discard, pr, nullLogger{})
	rec := newRecorder(recorderEnv(filepath.Join(notADir, "records")), 1, tr, nil, nullLogger{})
	got := make(chan json.RawMessage, 1)
	go func() {
		resp, _ := tr.SendRequest(context.Background(), `1`, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		got <- resp
	}()
	waitPending(t, tr, 1)
	if s := rec.record(disconnectlog.KindSignal, disconnectlog.ClosedByExternal, nil); !strings.Contains(s, "could not be written") {
		t.Errorf("summary = %q", s)
	}
	select {
	case resp := <-got:
		if !strings.Contains(string(resp), "could not be written") {
			t.Errorf("response %s lacks the reason", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request hung")
	}
}

func TestFailAllLeavesAbandonedEntriesAndListsThemApart(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	tr := newTranslator(io.Discard, pr, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tr.SendRequest(ctx, `1`, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`))
	}()
	waitPending(t, tr, 1)
	cancel()
	<-done // id 1 is now abandoned
	go func() {
		_, _ = tr.SendRequest(context.Background(), `2`, []byte(`{"jsonrpc":"2.0","id":2,"method":"resources/read"}`))
	}()
	waitPending(t, tr, 1)

	live, abandoned := tr.inFlight()
	if len(live) != 1 || live[0].ID != "2" || live[0].Method != "resources/read" {
		t.Errorf("live = %+v", live)
	}
	if len(abandoned) != 1 || abandoned[0].ID != "1" || abandoned[0].Method != "tools/call" {
		t.Errorf("abandoned = %+v", abandoned)
	}
	if n := tr.failAll("gone"); n != 1 {
		t.Errorf("failAll failed %d requests, want only the live one", n)
	}
	if _, abandoned := tr.inFlight(); len(abandoned) != 1 {
		t.Errorf("the abandoned entry must stay so its late response is dropped, got %+v", abandoned)
	}
}

func TestStdinFailureAndStdoutEOFAreSignalled(t *testing.T) {
	failing := &failWriter{}
	tr := newTranslator(failing, strings.NewReader(""), nullLogger{})
	signalled := make(chan struct{}, 1)
	tr.onStdinFail = func() { signalled <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()
	if err := tr.SendNotification([]byte(`{"jsonrpc":"2.0","method":"x"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-signalled:
	case <-time.After(5 * time.Second):
		t.Fatal("a failed stdin write was not signalled")
	}
	select {
	case <-tr.readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stdout EOF was not signalled")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTeeStderrCopiesEveryByteAndKeepsAScrubbedTail(t *testing.T) {
	var dst bytes.Buffer
	tail := disconnectlog.NewRing()
	input := "starting\nTOKEN=hunter2 loaded\n" + strings.Repeat("x", 100000) + "\nno newline at the end"
	teeStderr(strings.NewReader(input), &dst, tail)
	// Every line reaches the host log unchanged; the unterminated last line
	// gets its newline.
	if want := input + "\n"; dst.String() != want {
		t.Errorf("the host-side copy differs from the child's stderr: %d bytes vs %d", dst.Len(), len(want))
	}
	lines := tail.Snapshot()
	if len(lines) != 4 {
		t.Fatalf("tail has %d lines, want 4: %+v", len(lines), lines)
	}
	if strings.Contains(lines[1].Line, "hunter2") || !strings.Contains(lines[1].Line, "TOKEN=<redacted>") {
		t.Errorf("tail line = %q; want the value scrubbed", lines[1].Line)
	}
	if len(lines[2].Line) > disconnectlog.StderrLineBytes {
		t.Errorf("a 100000-byte line is %d bytes in the tail; want at most %d", len(lines[2].Line), disconnectlog.StderrLineBytes)
	}
	if lines[3].Line != "no newline at the end" {
		t.Errorf("the unterminated last line = %q", lines[3].Line)
	}
}

func TestExitOfReadsCodesAndSignals(t *testing.T) {
	if x := exitOf(nil); x.Code != 0 || x.Signal != "" {
		t.Errorf("exitOf(nil) = %+v", x)
	}
	exited := exec.Command("/bin/sh", "-c", "exit 3").Run()
	if x := exitOf(exited); x.Code != 3 || x.Signal != "" {
		t.Errorf("exit 3 = %+v", x)
	}
	killed := exec.Command("/bin/sh", "-c", "kill -9 $$").Run()
	if x := exitOf(killed); x.Signal != "killed" {
		t.Errorf("kill -9 = %+v; want signal killed", x)
	}
	if x := exitOf(io.ErrClosedPipe); x.Code != -1 {
		t.Errorf("a non-exit error = %+v", x)
	}
}

func TestAwaitChildSeesAnExitAndLeavesItForTerminate(t *testing.T) {
	done := make(chan error, 1)
	if exited, _ := awaitChild(done, 20*time.Millisecond); exited {
		t.Error("no exit yet, awaitChild said exited")
	}
	done <- io.ErrUnexpectedEOF
	if exited, err := awaitChild(done, time.Second); !exited || err != io.ErrUnexpectedEOF {
		t.Errorf("awaitChild = %v, %v", exited, err)
	}
	select {
	case err := <-done:
		if err != io.ErrUnexpectedEOF {
			t.Errorf("refilled %v", err)
		}
	default:
		t.Error("awaitChild must leave the exit in the channel for terminate")
	}
}

// The host's note that the coming SIGTERM is the end of the session: POST
// only, loopback origins only, and on the serve socket the frontend's.
func TestShutdownNoticeRoute(t *testing.T) {
	tr := newTranslator(io.Discard, strings.NewReader(""), nullLogger{})
	h := newHTTPHandler(tr, nullLogger{}, nil)
	mux := newMux(h, tr, make(chan error), false)
	do := func(method, origin string) int {
		req := httptest.NewRequest(method, "/clown/shutdown", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	if got := do(http.MethodGet, ""); got != http.StatusMethodNotAllowed || h.shutdownNoticed.Load() {
		t.Errorf("GET = %d (noticed %v); want 405 and no notice", got, h.shutdownNoticed.Load())
	}
	if got := do(http.MethodPost, "https://evil.example"); got != http.StatusForbidden || h.shutdownNoticed.Load() {
		t.Errorf("foreign origin = %d (noticed %v); want 403 and no notice", got, h.shutdownNoticed.Load())
	}
	if got := do(http.MethodPost, ""); got != http.StatusNoContent || !h.shutdownNoticed.Load() {
		t.Errorf("POST = %d (noticed %v); want 204 and the notice recorded", got, h.shutdownNoticed.Load())
	}

	// On the serve socket a caller without the frontend's peer class is
	// refused, like the other control routes.
	serve := newMux(newHTTPHandler(tr, nullLogger{}, nil), tr, make(chan error), true)
	w := httptest.NewRecorder()
	serve.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/clown/shutdown", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("serve-mode POST without a frontend peer = %d, want 403", w.Code)
	}
}

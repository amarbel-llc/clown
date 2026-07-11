package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

// --- sendPrompt unit tests (no juggler daemon involved) ---

// TestSendPrompt_SingleTextBlock verifies the outgoing request shape
// (method, path, headers, JSON body) and single-block text extraction.
func TestSendPrompt_SingleTextBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want 2023-06-01", got)
		}
		if got := r.Header.Get("x-api-key"); got != "sekret" {
			t.Errorf("x-api-key = %q, want sekret", got)
		}
		if got := r.Header.Get("content-type"); got != "application/json" {
			t.Errorf("content-type = %q, want application/json", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var req anthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if req.Model != "my-model" {
			t.Errorf("model = %q, want my-model", req.Model)
		}
		if req.MaxTokens != 512 {
			t.Errorf("max_tokens = %d, want 512", req.MaxTokens)
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hello there" {
			t.Errorf("messages = %+v", req.Messages)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi back"}]}`))
	}))
	defer srv.Close()

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL, Token: "sekret", Style: "anthropic"}
	got, err := sendPrompt(context.Background(), srv.Client(), resolved, "my-model", "hello there", 512)
	if err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if got != "hi back" {
		t.Errorf("got %q, want %q", got, "hi back")
	}
}

// TestSendPrompt_MultipleTextBlocksConcatenated verifies multi-block
// responses are concatenated in order.
func TestSendPrompt_MultipleTextBlocksConcatenated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"foo"},{"type":"text","text":"bar"}]}`))
	}))
	defer srv.Close()

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}
	got, err := sendPrompt(context.Background(), srv.Client(), resolved, "m", "p", 256)
	if err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if got != "foobar" {
		t.Errorf("got %q, want %q", got, "foobar")
	}
}

// TestSendPrompt_IgnoresNonTextBlocks verifies non-"text" content blocks
// (e.g. tool_use) are skipped rather than concatenated in.
func TestSendPrompt_IgnoresNonTextBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"foo"},{"type":"tool_use","text":"ignored"}]}`))
	}))
	defer srv.Close()

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}
	got, err := sendPrompt(context.Background(), srv.Client(), resolved, "m", "p", 256)
	if err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if got != "foo" {
		t.Errorf("got %q, want %q", got, "foo")
	}
}

// TestSendPrompt_NonSuccessStatus verifies a non-2xx response is
// surfaced as an error mentioning both the status code and the raw body.
func TestSendPrompt_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}
	_, err := sendPrompt(context.Background(), srv.Client(), resolved, "m", "p", 256)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should mention status code + body: %v", err)
	}
}

// TestSendPrompt_LocalUsesDummyToken verifies a local (Kind ==
// ModelKindLocal) result sends x-api-key: dummy regardless of Token,
// mirroring applyNamedProfile's local-instance dummy-auth convention.
func TestSendPrompt_LocalUsesDummyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "dummy" {
			t.Errorf("x-api-key = %q, want dummy", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: srv.URL}
	if _, err := sendPrompt(context.Background(), srv.Client(), resolved, "m", "p", 256); err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
}

// TestSendPrompt_OpenAICompatStyleRejected verifies a remote result with
// Style "openai-compat" is rejected with a clear error and, critically,
// no HTTP call is ever attempted — asserted via an atomic counter on the
// handler, matching this session's other concurrency/no-call-attempted
// proving pattern.
func TestSendPrompt_OpenAICompatStyleRejected(t *testing.T) {
	var called int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resolved := rm.ResolveModelResult{Kind: rm.ModelKindRemote, URL: srv.URL, Token: "t", Style: "openai-compat"}
	_, err := sendPrompt(context.Background(), srv.Client(), resolved, "m", "p", 256)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "openai-compat") || !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("error = %v", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("HTTP handler should never have been invoked, called %d times", called)
	}
}

// --- cmdPrompt integration tests (fake UDS juggler daemon + httptest.Server) ---

// fakeResolveModelDaemon starts a fake juggler control-socket listener
// (mirroring the harness in model_test.go/server_test.go) that answers
// exactly one ResolveModel-shaped RPC call with result. Sets
// JUGGLER_SOCKET via t.Setenv so dialClient() finds it.
func fakeResolveModelDaemon(t *testing.T, result rm.ResolveModelResult) {
	t.Helper()
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := rm.ReadFrame(br)
		if err != nil {
			return
		}
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return
		}
		_ = rm.WriteFrame(conn, rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: resultJSON})
	}()
	t.Setenv("JUGGLER_SOCKET", socket)
}

// TestCmdPrompt_HappyPath exercises the full flow: arg parsing -> resolve
// via the fake UDS daemon -> send via the httptest server -> print.
func TestCmdPrompt_HappyPath(t *testing.T) {
	var gotPrompt string
	remoteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		_ = json.Unmarshal(body, &req)
		if len(req.Messages) == 1 {
			gotPrompt = req.Messages[0].Content
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hello from model"}]}`))
	}))
	defer remoteSrv.Close()

	fakeResolveModelDaemon(t, rm.ResolveModelResult{
		Kind:  rm.ModelKindRemote,
		URL:   remoteSrv.URL,
		Token: "sekret",
		Style: "anthropic",
	})

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli, err := dialClient()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdPrompt(cli, []string{"my-model", "hello", "there"})
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if strings.TrimSpace(buf.String()) != "hello from model" {
		t.Errorf("stdout = %q", buf.String())
	}
	if gotPrompt != "hello there" {
		t.Errorf("prompt sent to model = %q, want %q", gotPrompt, "hello there")
	}
}

// TestCmdPrompt_StyleNotSupported exercises a registered remote model
// with Style "openai-compat": cmdPrompt should error clearly and the
// httptest server's handler must never be invoked.
func TestCmdPrompt_StyleNotSupported(t *testing.T) {
	var called int32
	remoteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer remoteSrv.Close()

	fakeResolveModelDaemon(t, rm.ResolveModelResult{
		Kind:  rm.ModelKindRemote,
		URL:   remoteSrv.URL,
		Token: "sekret",
		Style: "openai-compat",
	})

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cli, err := dialClient()
	if err != nil {
		os.Stderr = oldStderr
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdPrompt(cli, []string{"my-model", "hello"})
	cli.Close()
	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "openai-compat") || !strings.Contains(buf.String(), "not yet supported") {
		t.Errorf("stderr = %q", buf.String())
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("httptest handler should never have been invoked, called %d times", called)
	}
}

// TestCmdPrompt_StdinPrompt exercises the no-positional-prompt-text path:
// with no trailing args after the model name, cmdPrompt should read the
// prompt from stdin instead.
func TestCmdPrompt_StdinPrompt(t *testing.T) {
	var gotPrompt string
	remoteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		_ = json.Unmarshal(body, &req)
		if len(req.Messages) == 1 {
			gotPrompt = req.Messages[0].Content
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ack"}]}`))
	}))
	defer remoteSrv.Close()

	fakeResolveModelDaemon(t, rm.ResolveModelResult{Kind: rm.ModelKindLocal, URL: remoteSrv.URL})

	oldStdin := os.Stdin
	stdinR, stdinW, _ := os.Pipe()
	_, _ = stdinW.WriteString("hi from stdin\n")
	stdinW.Close()
	os.Stdin = stdinR

	oldStdout := os.Stdout
	outR, outW, _ := os.Pipe()
	os.Stdout = outW

	cli, err := dialClient()
	if err != nil {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdPrompt(cli, []string{"my-model"})
	cli.Close()
	os.Stdin = oldStdin
	os.Stdout = oldStdout
	outW.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, outR)
	if strings.TrimSpace(buf.String()) != "ack" {
		t.Errorf("stdout = %q", buf.String())
	}
	if gotPrompt != "hi from stdin" {
		t.Errorf("prompt sent to model = %q, want %q", gotPrompt, "hi from stdin")
	}
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

// TestCmdModelListFormatsUnion exercises cmdModelList's populated-table
// path: one local + one remote entry come back from ListModels, and the
// printed table should show both, tagged by kind and (for the remote
// entry) style.
func TestCmdModelListFormatsUnion(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

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
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: []byte(`{"models":[
				{"name":"qwen3-coder","kind":"local"},
				{"name":"gpt-4o","kind":"remote","style":"openai-compat"}
			]}`),
		})
	}()
	t.Setenv("JUGGLER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli, err := dialClient()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdModelList(cli)
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("expected rc=0, got %d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	got := buf.String()

	for _, want := range []string{
		"NAME", "KIND", "STYLE",
		"qwen3-coder", "local",
		"gpt-4o", "remote", "openai-compat",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestCmdModelListError exercises the RPC-failure path: cmdModelList
// should surface a nonzero rc and mention the error rather than panic
// on a nil/zero-value result.
func TestCmdModelListError(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

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
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rm.Error{Code: 1, Message: "boom"},
		})
	}()
	t.Setenv("JUGGLER_SOCKET", socket)

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cli, err := dialClient()
	if err != nil {
		os.Stderr = oldStderr
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdModelList(cli)
	cli.Close()
	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("stderr should mention error: %s", buf.String())
	}
}

// TestCmdModelAddRequiresURLAndToken exercises cmdModelAdd's usage-error
// path: `juggler model add <name> --style anthropic` with no --url (and
// no --token) should print a usage error and return a nonzero rc without
// making an RPC call. Passing a nil client makes "no call attempted"
// explicit — a bug that tried to dial nil would panic, failing the test.
func TestCmdModelAddRequiresURLAndToken(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdModelAdd(nil, []string{"my-model", "--style", "anthropic"})

	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "usage") {
		t.Errorf("stderr should mention usage: %s", buf.String())
	}
}

// TestCmdModelAdd_Basic exercises the happy path: all required flags
// present, RPC issued with the right params, and the success message
// mentions the model name.
func TestCmdModelAdd_Basic(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	gotParams := make(chan rm.AddRemoteModelParams, 1)
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
		var p rm.AddRemoteModelParams
		_ = json.Unmarshal(req.Params, &p)
		gotParams <- p
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{}`),
		})
	}()
	t.Setenv("JUGGLER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli, err := dialClient()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdModelAdd(cli, []string{
		"gpt-4o", "--style", "openai-compat",
		"--url", "https://example.test/v1",
		"--token", "sekret",
	})
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "gpt-4o") {
		t.Errorf("stdout missing model name: %s", buf.String())
	}

	select {
	case p := <-gotParams:
		if p.Name != "gpt-4o" || p.Style != "openai-compat" || p.URL != "https://example.test/v1" || p.Token != "sekret" {
			t.Errorf("params: %+v", p)
		}
	default:
		t.Fatal("no params received")
	}
}

// TestCmdModelAdd_EqualsForms verifies --style=/--url=/--token= parse
// identically to the space-separated forms.
func TestCmdModelAdd_EqualsForms(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	gotParams := make(chan rm.AddRemoteModelParams, 1)
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, _ := rm.ReadFrame(br)
		var p rm.AddRemoteModelParams
		_ = json.Unmarshal(req.Params, &p)
		gotParams <- p
		_ = rm.WriteFrame(conn, rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: []byte(`{}`)})
	}()
	t.Setenv("JUGGLER_SOCKET", socket)

	cli, err := dialClient()
	if err != nil {
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdModelAdd(cli, []string{
		"m", "--style=anthropic", "--url=https://x.test", "--token=tok",
	})
	cli.Close()
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}

	p := <-gotParams
	if p.Style != "anthropic" || p.URL != "https://x.test" || p.Token != "tok" {
		t.Errorf("params: %+v", p)
	}
}

// TestCmdModelAdd_InvalidStyle exercises cmdModelAdd's --style enum
// validation: a style that is neither "anthropic" nor "openai-compat"
// should be a usage error, no RPC call. Passing a nil client makes "no
// call attempted" explicit, same as TestCmdModelAddRequiresURLAndToken.
func TestCmdModelAdd_InvalidStyle(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdModelAdd(nil, []string{
		"my-model", "--style", "bogus-style",
		"--url", "https://example.test", "--token", "tok",
	})

	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "bogus-style") {
		t.Errorf("stderr should mention the invalid style: %s", buf.String())
	}
}

// TestCmdModelAdd_UnknownFlag exercises cmdModelAdd's unrecognized-flag
// rejection: an unknown flag (e.g. --bogus) alongside otherwise-complete
// required flags should still be rejected before any RPC call is made.
// Passing a nil client makes "no call attempted" explicit, same as
// TestCmdModelAddRequiresURLAndToken.
func TestCmdModelAdd_UnknownFlag(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdModelAdd(nil, []string{
		"my-model", "--style", "x", "--url", "y", "--token", "z",
		"--bogus", "foo",
	})

	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "unknown flag") {
		t.Errorf("stderr should mention the unrecognized flag: %s", buf.String())
	}
}

// TestCmdModelAdd_MissingName exercises the missing-positional-arg path:
// no name given at all should be a usage error, no RPC call.
func TestCmdModelAdd_MissingName(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdModelAdd(nil, []string{})

	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "usage") {
		t.Errorf("stderr should mention usage: %s", buf.String())
	}
}

// TestCmdModelRemove_Basic exercises the happy path for `model remove`.
func TestCmdModelRemove_Basic(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	gotParams := make(chan rm.RemoveRemoteModelParams, 1)
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
		var p rm.RemoveRemoteModelParams
		_ = json.Unmarshal(req.Params, &p)
		gotParams <- p
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{}`),
		})
	}()
	t.Setenv("JUGGLER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli, err := dialClient()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdModelRemove(cli, []string{"gpt-4o"})
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "gpt-4o") {
		t.Errorf("stdout missing model name: %s", buf.String())
	}

	select {
	case p := <-gotParams:
		if p.Name != "gpt-4o" {
			t.Errorf("params: %+v", p)
		}
	default:
		t.Fatal("no params received")
	}
}

// TestCmdModelRemove_MissingName exercises the missing-arg path: no name
// given should be a usage error, no RPC call.
func TestCmdModelRemove_MissingName(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdModelRemove(nil, []string{})

	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "usage") {
		t.Errorf("stderr should mention usage: %s", buf.String())
	}
}

// TestCmdModel_UnknownSubcommand exercises the dispatch default case.
func TestCmdModel_UnknownSubcommand(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdModel(nil, []string{"bogus"})

	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "usage") {
		t.Errorf("stderr should mention usage: %s", buf.String())
	}
}

// TestCmdModel_NoArgs exercises the no-subcommand-given path.
func TestCmdModel_NoArgs(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdModel(nil, []string{})

	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected nonzero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "usage") {
		t.Errorf("stderr should mention usage: %s", buf.String())
	}
}

package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// fakeRingmaster spins up a Unix socket listener that responds to one
// request with the supplied result/error envelope, then closes the
// connection. The test selects which response shape via the helper used.
//
// shortTempSocket is defined in list_test.go (same package); reused here.

func TestCmdStatus_EmptyList(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go fakeServeOnce(t, ln, `{"instances":[]}`)
	t.Setenv("RINGMASTER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli, err := dialClient()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdStatus(cli, nil)
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("expected rc=0, got %d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "no instances running") {
		t.Errorf("expected 'no instances running' message, got: %s", buf.String())
	}
}

func TestCmdStatus_TwoInstances(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	startedAt := time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339Nano)
	body := `{"instances":[
        {"alias":"a","model":"a","port":1,"pid":2,"bind":"127.0.0.1","started_at":"` + startedAt + `"},
        {"alias":"b","model":"b","port":3,"pid":4,"bind":"127.0.0.1","started_at":"` + startedAt + `"}
    ]}`
	go fakeServeOnce(t, ln, body)
	t.Setenv("RINGMASTER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cli, _ := dialClient()
	rc := cmdStatus(cli, nil)
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	got := buf.String()
	for _, want := range []string{"ALIAS", "a", "b"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestCmdStatus_SingleAlias(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	startedAt := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339Nano)
	body := `{"instance":{"alias":"qwen","model":"qwen","port":43219,"pid":91234,"bind":"127.0.0.1","started_at":"` + startedAt + `"}}`
	go fakeServeOnce(t, ln, body)
	t.Setenv("RINGMASTER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cli, _ := dialClient()
	rc := cmdStatus(cli, []string{"qwen"})
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	got := buf.String()
	for _, want := range []string{
		"alias:", "qwen", "127.0.0.1", "43219", "91234", "started:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestCmdStatus_AliasNotFound(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Respond with the RPC error shape for alias-not-found.
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, _ := rm.ReadFrame(br)
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &rm.Error{
				Code:    -32001,
				Message: `alias "missing" not found`,
			},
		})
	}()
	t.Setenv("RINGMASTER_SOCKET", socket)

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cli, _ := dialClient()
	rc := cmdStatus(cli, []string{"missing"})
	cli.Close()
	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected non-zero rc for not-found, got 0")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "not found") {
		t.Errorf("expected 'not found' in stderr, got: %s", buf.String())
	}
}

// fakeServeOnce reads one request frame and writes the supplied raw JSON
// as the Result. Closes the connection after.
//
// Runs from a goroutine, so t.Helper / t.Fatal don't apply; surface
// errors via t.Logf instead, so test failures point at the actual I/O
// problem rather than at a downstream "RPC timed out" or "wrong rc"
// symptom. Listener-close at end-of-test is the expected exit, so its
// net.ErrClosed is silenced.
func fakeServeOnce(t *testing.T, ln net.Listener, resultJSON string) {
	conn, err := ln.Accept()
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			t.Logf("fakeServeOnce: Accept: %v", err)
		}
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	req, err := rm.ReadFrame(br)
	if err != nil {
		t.Logf("fakeServeOnce: ReadFrame: %v", err)
		return
	}
	if err := rm.WriteFrame(conn, rm.Envelope{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  []byte(resultJSON),
	}); err != nil {
		t.Logf("fakeServeOnce: WriteFrame: %v", err)
	}
}

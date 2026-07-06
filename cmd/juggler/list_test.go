package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

// shortTempSocket returns a unix-socket path inside a short-path tmpdir
// (macOS caps sun_path at ~104 bytes; the devshell's in-worktree TMPDIR
// can exceed it). Delegates to shortTempDir (server_test.go, same
// package) for the tmpdir + cleanup.
func shortTempSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempDir(t), "control.sock")
}

// TestCmdList_Empty exercises cmdList's empty-result path: juggler
// returns no instances, cmdList should exit 0 and print nothing
// (besides whatever tabwriter would emit for an empty body, which is
// nothing because the header is skipped on len==0).
func TestCmdList_Empty(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Fake server: accept one connection, reply with empty list.
	srvDone := make(chan struct{})
	go func() {
		defer close(srvDone)
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
			Result:  []byte(`{"instances":[]}`),
		})
	}()

	t.Setenv("JUGGLER_SOCKET", socket)

	cli, err := dialClient()
	if err != nil {
		t.Fatalf("dialClient: %v", err)
	}
	defer cli.Close()

	rc := cmdList(cli)
	if rc != 0 {
		t.Errorf("expected rc=0, got %d", rc)
	}
	<-srvDone
}

// TestCmdList_TwoInstances exercises the populated-table path: two
// instances come back, cmdList prints the header + one row each.
func TestCmdList_TwoInstances(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	startedAt := time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339Nano)
	resultJSON := `{"instances":[
        {"alias":"qwen3-coder","model":"qwen3-coder","port":43219,"pid":91234,"bind":"127.0.0.1","started_at":"` + startedAt + `"},
        {"alias":"gemma-3-270m","model":"gemma-3-270m","port":43221,"pid":91241,"bind":"127.0.0.1","started_at":"` + startedAt + `"}
    ]}`

	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, _ := rm.ReadFrame(br)
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(resultJSON),
		})
	}()

	t.Setenv("JUGGLER_SOCKET", socket)

	// Capture stdout.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli, err := dialClient()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdList(cli)
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
		"ALIAS", "MODEL", "BIND", "PORT", "PID", "UPTIME",
		"qwen3-coder", "gemma-3-270m", "127.0.0.1", "43219", "43221",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

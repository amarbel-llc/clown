package ringmaster

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// shortTempDir returns a short-path tmpdir. macOS imposes a ~104-char
// limit on Unix domain socket paths (sun_path), and the project's
// devshell sets TMPDIR inside the worktree, which can exceed it. Use
// /tmp explicitly and clean up via t.Cleanup.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ringmaster-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestClient_ListInstances_Empty(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Fake server: accept one connection, reply with empty list.
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(bufio.NewReader(conn))
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"instances":[]}`),
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Instances) != 0 {
		t.Errorf("expected empty, got %+v", res.Instances)
	}
}

func TestClient_RPCError(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Fake server: reply with a JSON-RPC error envelope.
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(bufio.NewReader(conn))
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &Error{
				Code:    -32601,
				Message: "method not found",
			},
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	_, err = cli.ListInstances(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Sanity-check that the message surfaces.
	if msg := err.Error(); msg == "" {
		t.Fatalf("error has empty message: %v", err)
	}
}

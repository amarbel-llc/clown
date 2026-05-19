package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func TestCmdModels_Empty(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Logf("Accept: %v", err)
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := rm.ReadFrame(br)
		if err != nil {
			t.Logf("ReadFrame: %v", err)
			return
		}
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"models":[]}`),
		})
	}()
	t.Setenv("RINGMASTER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cli, _ := dialClient()
	rc := cmdModels(cli)
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if buf.Len() != 0 {
		t.Errorf("expected no output on empty list, got %q", buf.String())
	}
}

func TestCmdModels_TwoEntries(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, _ := rm.ReadFrame(br)
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: []byte(`{"models":[
                {"name":"alpha","path":"/x/alpha.gguf","size":1024},
                {"name":"beta","path":"/x/beta.gguf","size":2048}
            ]}`),
		})
	}()
	t.Setenv("RINGMASTER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cli, _ := dialClient()
	rc := cmdModels(cli)
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	// One name per line, no headers, no sizes/paths visible.
	want := "alpha\nbeta\n"
	if out != want {
		t.Errorf("output mismatch:\n got %q\nwant %q", out, want)
	}
	// Sanity: shouldn't include size or path.
	if strings.Contains(out, "1024") || strings.Contains(out, ".gguf") {
		t.Errorf("output unexpectedly contains size/path: %q", out)
	}
}

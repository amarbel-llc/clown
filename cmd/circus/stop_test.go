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

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func TestCmdStop_Basic(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	gotParams := make(chan rm.StopInstanceParams, 1)
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, _ := rm.ReadFrame(br)
		var p rm.StopInstanceParams
		_ = json.Unmarshal(req.Params, &p)
		gotParams <- p
		// StopInstance returns null result (matching server.go's StopInstance dispatch).
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0", ID: req.ID, Result: []byte("null"),
		})
	}()
	t.Setenv("RINGMASTER_SOCKET", socket)

	cli, _ := dialClient()
	rc := cmdStop(cli, []string{"qwen3-coder"})
	cli.Close()
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}

	p := <-gotParams
	if p.Alias != "qwen3-coder" {
		t.Errorf("alias=%q", p.Alias)
	}
}

func TestCmdStop_MissingAlias(t *testing.T) {
	// cmdStop's missing-alias path bails before touching the client.
	// Pass nil to make that contract explicit.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdStop(nil, []string{})
	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected non-zero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "alias") {
		t.Errorf("stderr should mention 'alias': %s", buf.String())
	}
}

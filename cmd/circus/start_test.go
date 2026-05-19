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
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

func TestCmdStart_Basic(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Capture what params the client sends.
	gotParams := make(chan rm.StartInstanceParams, 1)
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
		var p rm.StartInstanceParams
		_ = json.Unmarshal(req.Params, &p)
		gotParams <- p
		startedAt := time.Now().UTC().Format(time.RFC3339Nano)
		body := `{"instance":{"alias":"` + p.Alias + `","model":"` + p.Model +
			`","port":4321,"pid":99,"bind":"127.0.0.1","started_at":"` + startedAt + `"}}`
		_ = rm.WriteFrame(conn, rm.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(body),
		})
	}()
	t.Setenv("RINGMASTER_SOCKET", socket)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli, err := dialClient()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("dialClient: %v", err)
	}
	rc := cmdStart(cli, []string{"qwen3-coder"})
	cli.Close()
	os.Stdout = oldStdout
	w.Close()

	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "qwen3-coder") {
		t.Errorf("stdout missing alias: %s", out)
	}
	if !strings.Contains(out, "127.0.0.1:4321") {
		t.Errorf("stdout missing host:port: %s", out)
	}

	select {
	case p := <-gotParams:
		if p.Model != "qwen3-coder" {
			t.Errorf("model=%q", p.Model)
		}
		if p.Alias != "qwen3-coder" {
			t.Errorf("alias should default to model, got %q", p.Alias)
		}
		if p.Bind != "" {
			t.Errorf("bind should be empty when not provided, got %q", p.Bind)
		}
	case <-time.After(time.Second):
		t.Fatal("no params received")
	}
}

func TestCmdStart_AliasAndBindAndPassthrough(t *testing.T) {
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	gotParams := make(chan rm.StartInstanceParams, 1)
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, _ := rm.ReadFrame(br)
		var p rm.StartInstanceParams
		_ = json.Unmarshal(req.Params, &p)
		gotParams <- p
		startedAt := time.Now().UTC().Format(time.RFC3339Nano)
		body := `{"instance":{"alias":"` + p.Alias + `","model":"` + p.Model + `","port":1,"pid":2,"bind":"` + p.Bind + `","started_at":"` + startedAt + `"}}`
		_ = rm.WriteFrame(conn, rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: []byte(body)})
	}()
	t.Setenv("RINGMASTER_SOCKET", socket)

	cli, _ := dialClient()
	rc := cmdStart(cli, []string{
		"qwen3-coder", "--alias", "coder-32k",
		"--bind", "0.0.0.0",
		"--", "--ctx-size", "32768",
	})
	cli.Close()
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}

	select {
	case p := <-gotParams:
		if p.Model != "qwen3-coder" || p.Alias != "coder-32k" || p.Bind != "0.0.0.0" {
			t.Errorf("params: %+v", p)
		}
		want := []string{"--ctx-size", "32768"}
		if len(p.Args) != 2 || p.Args[0] != want[0] || p.Args[1] != want[1] {
			t.Errorf("Args=%v want %v", p.Args, want)
		}
	case <-time.After(time.Second):
		t.Fatal("no params received")
	}
}

func TestCmdStart_MissingModel(t *testing.T) {
	// cmdStart's missing-model path bails before touching the client.
	// Pass nil to make that contract explicit and remove any risk of
	// the test failing in dialClient (e.g. on systems with very long
	// $TMPDIR) instead of in the assertion we care about.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	rc := cmdStart(nil, []string{})
	os.Stderr = oldStderr
	w.Close()

	if rc == 0 {
		t.Errorf("expected non-zero rc")
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "model") {
		t.Errorf("stderr should mention 'model': %s", buf.String())
	}
}

func TestCmdStart_EqualsForms(t *testing.T) {
	// Verify --alias=foo and --bind=bar forms parse identically.
	socket := shortTempSocket(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	gotParams := make(chan rm.StartInstanceParams, 1)
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, _ := rm.ReadFrame(br)
		var p rm.StartInstanceParams
		_ = json.Unmarshal(req.Params, &p)
		gotParams <- p
		startedAt := time.Now().UTC().Format(time.RFC3339Nano)
		body := `{"instance":{"alias":"` + p.Alias + `","model":"` + p.Model + `","port":1,"pid":2,"bind":"` + p.Bind + `","started_at":"` + startedAt + `"}}`
		_ = rm.WriteFrame(conn, rm.Envelope{JSONRPC: "2.0", ID: req.ID, Result: []byte(body)})
	}()
	t.Setenv("RINGMASTER_SOCKET", socket)

	cli, _ := dialClient()
	rc := cmdStart(cli, []string{"m", "--alias=a", "--bind=1.2.3.4"})
	cli.Close()
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}

	p := <-gotParams
	if p.Alias != "a" || p.Bind != "1.2.3.4" {
		t.Errorf("params: %+v", p)
	}
}

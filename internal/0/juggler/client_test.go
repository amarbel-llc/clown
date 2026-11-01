package juggler

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortTempDir returns a short-path tmpdir. macOS imposes a ~104-char
// limit on Unix domain socket paths (sun_path), and the project's
// devshell sets TMPDIR inside the worktree, which can exceed it. Use
// /tmp explicitly and clean up via t.Cleanup.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "juggler-test-")
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

func TestClientListModels(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Fake server: accept one connection, answer MethodListModels with a
	// canned ListModelsResult.
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(bufio.NewReader(conn))
		if req.Method != MethodListModels {
			WriteFrame(conn, Envelope{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &Error{Code: -32601, Message: "unexpected method: " + req.Method},
			})
			return
		}
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"models":[{"name":"local-a","kind":"local"},{"name":"remote-a","kind":"remote","style":"anthropic"}]}`),
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	res, err := cli.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ListModelsResult{
		Models: []Model{
			{Name: "local-a", Kind: ModelKindLocal},
			{Name: "remote-a", Kind: ModelKindRemote, Style: "anthropic"},
		},
	}
	if len(res.Models) != len(want.Models) {
		t.Fatalf("expected %d models, got %+v", len(want.Models), res.Models)
	}
	for i := range want.Models {
		if res.Models[i] != want.Models[i] {
			t.Errorf("model %d: expected %+v, got %+v", i, want.Models[i], res.Models[i])
		}
	}
}

func TestClientAddRemoteModel(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var gotParams AddRemoteModelParams
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(bufio.NewReader(conn))
		if req.Method != MethodAddRemoteModel {
			WriteFrame(conn, Envelope{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &Error{Code: -32601, Message: "unexpected method: " + req.Method},
			})
			return
		}
		_ = json.Unmarshal(req.Params, &gotParams)
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{}`),
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	p := AddRemoteModelParams{Name: "remote-a", Style: "anthropic", URL: "https://example.com", Token: "${TOKEN}"}
	if err := cli.AddRemoteModel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if gotParams != p {
		t.Errorf("expected server to receive %+v, got %+v", p, gotParams)
	}
}

func TestClientRemoveRemoteModel(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var gotParams RemoveRemoteModelParams
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(bufio.NewReader(conn))
		if req.Method != MethodRemoveRemoteModel {
			WriteFrame(conn, Envelope{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &Error{Code: -32601, Message: "unexpected method: " + req.Method},
			})
			return
		}
		_ = json.Unmarshal(req.Params, &gotParams)
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  nil,
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	p := RemoveRemoteModelParams{Name: "remote-a"}
	if err := cli.RemoveRemoteModel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if gotParams != p {
		t.Errorf("expected server to receive %+v, got %+v", p, gotParams)
	}
}

func TestClientResolveModel(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var gotParams ResolveModelParams
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(bufio.NewReader(conn))
		if req.Method != MethodResolveModel {
			WriteFrame(conn, Envelope{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &Error{Code: -32601, Message: "unexpected method: " + req.Method},
			})
			return
		}
		_ = json.Unmarshal(req.Params, &gotParams)
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"kind":"remote","url":"https://example.com","token":"secret-value","style":"anthropic"}`),
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	p := ResolveModelParams{Name: "remote-a"}
	res, err := cli.ResolveModel(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := ResolveModelResult{Kind: ModelKindRemote, URL: "https://example.com", Token: "secret-value", Style: "anthropic"}
	if res != want {
		t.Errorf("expected %+v, got %+v", want, res)
	}
	if gotParams != p {
		t.Errorf("expected server to receive %+v, got %+v", p, gotParams)
	}
}

func TestClient_RPCIDMismatch(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Fake server: reply with a different ID than the client sent.
	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		req, _ := ReadFrame(bufio.NewReader(conn))
		_ = req
		WriteFrame(conn, Envelope{
			JSONRPC: "2.0",
			ID:      json.Number("999"),
			Result:  []byte(`{"instances":[]}`),
		})
	}()

	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	_, err = cli.ListInstances(context.Background())
	if err == nil || !strings.Contains(err.Error(), "id mismatch") {
		t.Errorf("expected id mismatch error, got: %v", err)
	}
}

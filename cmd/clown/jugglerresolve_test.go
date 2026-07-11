package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/clown/internal/juggler"
)

// shortTempDir returns a short-path tmpdir. macOS imposes a ~104-char
// limit on Unix domain socket paths (sun_path), and the project's
// devshell sets TMPDIR inside the worktree, which can exceed it. Use
// /tmp explicitly and clean up via t.Cleanup. Mirrors the identical
// helper in internal/juggler/client_test.go (unexported there, so not
// reusable across packages).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "clown-jugglerresolve-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeJugglerServer starts a fake juggler control server on a fresh UDS
// socket and answers exactly one connection with handle. Returns the
// socket path; the listener is torn down via t.Cleanup.
func fakeJugglerServer(t *testing.T, handle func(conn net.Conn, req juggler.Envelope)) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "control.sock")
	ln, err := net.Listen("unix", sock)
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
		req, err := juggler.ReadFrame(bufio.NewReader(conn))
		if err != nil {
			return
		}
		handle(conn, req)
	}()

	return sock
}

func TestResolveViaJugglerUnreachableDaemon(t *testing.T) {
	t.Setenv("JUGGLER_SOCKET", filepath.Join(t.TempDir(), "no-such.sock"))
	_, err := resolveViaJuggler("anything")
	if err == nil || !strings.Contains(err.Error(), "juggler daemon") {
		t.Fatalf("want a daemon-unreachable error, got %v", err)
	}
}

func TestResolveViaJugglerSuccess(t *testing.T) {
	sock := fakeJugglerServer(t, func(conn net.Conn, req juggler.Envelope) {
		if req.Method != juggler.MethodResolveModel {
			_ = juggler.WriteFrame(conn, juggler.Envelope{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &juggler.Error{Code: -32601, Message: "unexpected method: " + req.Method},
			})
			return
		}
		_ = juggler.WriteFrame(conn, juggler.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"kind":"remote","url":"https://example.com","token":"secret-value","style":"anthropic"}`),
		})
	})
	t.Setenv("JUGGLER_SOCKET", sock)

	res, err := resolveViaJuggler("remote-a")
	if err != nil {
		t.Fatal(err)
	}
	want := juggler.ResolveModelResult{
		Kind:  juggler.ModelKindRemote,
		URL:   "https://example.com",
		Token: "secret-value",
		Style: "anthropic",
	}
	if res != want {
		t.Errorf("expected %+v, got %+v", want, res)
	}
}

func TestJugglerModelKindUnreachableDaemon(t *testing.T) {
	t.Setenv("JUGGLER_SOCKET", filepath.Join(t.TempDir(), "no-such.sock"))
	kind, ok := jugglerModelKind("anything")
	if ok {
		t.Fatalf("expected ok=false against an unreachable daemon, got kind=%q ok=%v", kind, ok)
	}
}

func TestJugglerModelKindFound(t *testing.T) {
	var gotMethod string
	sock := fakeJugglerServer(t, func(conn net.Conn, req juggler.Envelope) {
		gotMethod = req.Method
		if req.Method != juggler.MethodListModels {
			_ = juggler.WriteFrame(conn, juggler.Envelope{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &juggler.Error{Code: -32601, Message: "unexpected method: " + req.Method},
			})
			return
		}
		_ = juggler.WriteFrame(conn, juggler.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"models":[{"name":"local-a","kind":"local"},{"name":"remote-a","kind":"remote","style":"anthropic"}]}`),
		})
	})
	t.Setenv("JUGGLER_SOCKET", sock)

	kind, ok := jugglerModelKind("remote-a")
	if !ok || kind != juggler.ModelKindRemote {
		t.Fatalf("expected (remote, true), got (%q, %v)", kind, ok)
	}
	// The whole point of jugglerModelKind is that it is side-effect-free:
	// it must call ListModels, never ResolveModel.
	if gotMethod != juggler.MethodListModels {
		t.Fatalf("expected the server to see method %q, got %q", juggler.MethodListModels, gotMethod)
	}
}

func TestJugglerModelKindNotFound(t *testing.T) {
	sock := fakeJugglerServer(t, func(conn net.Conn, req juggler.Envelope) {
		_ = juggler.WriteFrame(conn, juggler.Envelope{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  []byte(`{"models":[{"name":"local-a","kind":"local"}]}`),
		})
	})
	t.Setenv("JUGGLER_SOCKET", sock)

	kind, ok := jugglerModelKind("claude-haiku-4-5")
	if ok {
		t.Fatalf("expected ok=false for a name absent from ListModels, got kind=%q", kind)
	}
}

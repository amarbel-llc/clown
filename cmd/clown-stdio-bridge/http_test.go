package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
)

// runTranslator sets up a translator over an in-memory pipe pair, with
// a goroutine that simulates the wrapped child's responses by reading
// stdin and emitting echo responses on stdoutW. Returns the translator
// and a cleanup func.
//
// The streamable-HTTP spine this feeds (POST/GET/SSE/heartbeats/origin) is
// tested in internal/mcphttp; the bridge's own tests exercise only the
// bridge-specific tool-exclusion surface, which still needs a live
// translator as the spine's RequestHandler.
func runTranslator(t *testing.T) (*translator, func()) {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	tr := newTranslator(stdinW, stdoutR, nullLogger{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = tr.Run(ctx) }()

	// Wrapped-child simulator: read each line from stdin, parse it,
	// echo a matching response on stdout. Notifications and responses
	// from the client are silently consumed (no-op).
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := stdinR.Read(tmp)
			if err != nil {
				return
			}
			buf = append(buf, tmp[:n]...)
			for {
				idx := bytes.IndexByte(buf, '\n')
				if idx < 0 {
					break
				}
				line := buf[:idx]
				buf = buf[idx+1:]
				var msg map[string]any
				if err := json.Unmarshal(line, &msg); err != nil {
					continue
				}
				if _, hasMethod := msg["method"]; hasMethod {
					if _, hasID := msg["id"]; hasID {
						resp := map[string]any{
							"jsonrpc": "2.0",
							"id":      msg["id"],
							"result":  map[string]any{"echoed_method": msg["method"]},
						}
						out, _ := json.Marshal(resp)
						_, _ = stdoutW.Write(append(out, '\n'))
					}
				}
			}
		}
	}()

	cleanup := func() {
		cancel()
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}
	return tr, cleanup
}

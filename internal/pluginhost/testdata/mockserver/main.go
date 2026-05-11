// Command mock-mcp-server is a minimal HTTP-transport MCP server used
// for integration-testing the plugin-host pipeline. It speaks
// JSON-RPC 2.0 on POST /mcp per the MCP streamable-http transport
// and emits a clown plugin-host handshake on stdout at startup.
//
// Supported methods (mirror of internal/pluginhost/testdata/mockstdiomcp,
// which mock-stdio-mcp uses over stdio for the bridge tests):
//
//   - initialize → returns minimal serverInfo / capabilities.
//   - tools/list → returns one mock tool ("mock-tool").
//   - tools/call (any tool) → returns "ok".
//   - notify-broadcast (test-only) → emits a server-initiated
//     notification then a result so callers can exercise the
//     SSE broadcast path.
//
// Any other method returns a JSON-RPC error -32601. Implementing the
// initialize round-trip is what makes `claude mcp list` report this
// server as Connected (a previous "always return tools/list"
// shortcut was reported as Failed-to-connect because claude's
// connectivity check sends initialize first).
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-mcp-server: listen: %v\n", err)
		os.Exit(1)
	}

	addr := ln.Addr().String()
	fmt.Printf("1|1|tcp|%s|streamable-http\n", addr)
	os.Stdout.Sync()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp", handleMCP)

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	srv.Close()
}

// handleMCP parses a single JSON-RPC 2.0 request from the body and
// writes a JSON-RPC 2.0 response. Non-POST methods get a 405; malformed
// JSON gets a -32700 parse error.
func handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      nil,
			"error":   map[string]any{"code": -32700, "message": fmt.Sprintf("parse error: %v", err)},
		})
		return
	}
	method, _ := req["method"].(string)
	id := req["id"]

	switch method {
	case "initialize":
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "mock-mcp-server", "version": "0.0.1"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			},
		})
	case "tools/list":
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"tools": []map[string]any{
					{"name": "mock-tool", "description": "A mock tool for testing"},
				},
			},
		})
	case "tools/call":
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}},
		})
	case "notifications/initialized":
		// MCP clients send this as a notification (no id) after a
		// successful initialize. Notifications don't get a response;
		// just acknowledge the request with 200.
		w.WriteHeader(http.StatusOK)
	default:
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32601, "message": fmt.Sprintf("unknown method %q", method)},
		})
	}
}

func writeJSON(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

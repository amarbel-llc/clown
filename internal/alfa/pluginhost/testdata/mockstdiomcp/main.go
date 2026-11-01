// Command mock-stdio-mcp is a minimal stdio JSON-RPC MCP server used
// for integration-testing clown-stdio-bridge. It speaks line-delimited
// JSON-RPC 2.0 on stdin/stdout per the MCP stdio transport.
//
// Supported methods:
//
//   - initialize → returns minimal serverInfo / capabilities.
//   - tools/list → returns one mock tool.
//   - tools/call (any tool) → returns "ok".
//   - notify-broadcast (test-only) → emits a server-initiated
//     notification on stdout; useful for exercising the bridge's SSE
//     broadcast path.
//
// Any other method returns a JSON-RPC error -32601 (method not found).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo":      map[string]any{"name": "mock-stdio-mcp", "version": "0.0.1"},
					"capabilities":    map[string]any{"tools": map[string]any{}},
				},
			})
		case "tools/list":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "echo", "description": "Echoes its input"},
					},
				},
			})
		case "tools/call":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}},
			})
		case "notify-broadcast":
			// Emit a server-initiated notification (no id) to exercise
			// the bridge's broadcast / SSE path.
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/tools/list_changed",
			})
			// Then respond to the caller so the HTTP request can return.
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"queued": true},
			})
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error":   map[string]any{"code": -32601, "message": fmt.Sprintf("unknown method %q", method)},
			})
		}
	}
}

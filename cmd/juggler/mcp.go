package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	rm "github.com/amarbel-llc/clown/internal/juggler"
)

// mcpScannerMaxLine bounds a single JSON-RPC line. Deliberately far above
// ringmaster jobmcp.Serve's 1MB (that server's params are all short
// structured fields — alias names, job ids). This server's `task`
// argument is a real delegated prompt that can reasonably embed several
// files' worth of context, so 1MB is realistic to hit; 16MB gives that
// kind of "real work" tool the headroom a job-control message doesn't
// need. If a line still exceeds this, sc.Scan() returns false and the
// loop below logs the failure to stderr rather than dying silently.
const mcpScannerMaxLine = 16 << 20

// mcpMaxTokensDefault is deliberately higher than juggler prompt's CLI
// default (256): the CLI is a quick smoke/hello-world tool, this tool is
// for real delegated subagent-shaped work. This session's own testing
// showed a reasoning model needs a much larger budget to get past its
// thinking tokens to an actual answer (docs/plans/2026-07-11-juggler-
// subagent-tool-design.md's Tuning Levers).
const mcpMaxTokensDefault = 1024

// mcpToolTimeout mirrors promptTimeout's rationale (may need to start a
// local instance) — one budget per tools/call.
const mcpToolTimeout = 120 * time.Second

// Serve runs the JSON-RPC 2.0 loop against in/out — the MCP stdio
// transport, hand-rolled to match ringmaster's jobmcp.Serve exactly (no
// MCP SDK is vendored in this repo). cli is used to resolve and prompt
// models on tools/call; it may be nil, in which case every tools/call
// reports the daemon as unreachable (used by tests exercising that path
// without a live socket).
func Serve(in io.Reader, out io.Writer, cli *rm.Client) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), mcpScannerMaxLine)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue // skip unparseable line (transport noise)
		}
		switch req.Method {
		case "initialize":
			_ = enc.Encode(mcpResult(req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "juggler", "version": "1"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}))
		case "tools/list":
			_ = enc.Encode(mcpResult(req.ID, map[string]any{"tools": []map[string]any{jugglerPromptToolSchema()}}))
		case "tools/call":
			_ = enc.Encode(mcpResult(req.ID, callJugglerPromptTool(cli, req.Params)))
		case "notifications/initialized":
			// Notification (no id): no response.
		default:
			if len(req.ID) > 0 {
				_ = enc.Encode(mcpError(req.ID, -32601, fmt.Sprintf("unknown method %q", req.Method)))
			}
		}
	}
	// sc.Scan() returning false ends the loop either at a clean EOF
	// (sc.Err() == nil, the normal stdio-transport shutdown when the
	// client closes stdin) or because a line exceeded mcpScannerMaxLine
	// or the underlying read failed. The latter would otherwise be a
	// silent, permanent death of every subsequent tools/call in this
	// process — stdout is the JSON-RPC channel, so the only place left to
	// say something is stderr.
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "juggler mcp: stdio scanner stopped: %v\n", err)
	}
}

func mcpResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func mcpError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

// mcpToolText/mcpToolErr mirror ringmaster jobmcp's toolText/toolErr
// convention: a tool-level failure is content with isError set, so the
// agent sees the message as tool output it can react to, not a transport
// error.
func mcpToolText(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func mcpToolErr(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true}
}

// jugglerPromptToolSchema is the single tool this server exposes — model
// as a parameter, not one tool per registered model, so the catalog never
// needs regenerating when models are added/removed (mirrors how Task's
// own subagent_type is a parameter, not one tool per subagent type).
func jugglerPromptToolSchema() map[string]any {
	return map[string]any{
		"name":        "juggler-prompt",
		"description": "Delegate a task to a registered juggler model (local or remote) and get its text reply.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model":      map[string]any{"type": "string", "description": "a name registered via `juggler model add`, or a local GGUF name"},
				"task":       map[string]any{"type": "string", "description": "the task/prompt to send"},
				"max_tokens": map[string]any{"type": "integer", "description": "default 1024"},
			},
			"required": []string{"model", "task"},
		},
	}
}

// callJugglerPromptTool unmarshals a tools/call request for juggler-prompt,
// resolves the model, and sends the task — reusing the exact same
// sendAnthropicPrompt/sendOpenAICompatPrompt functions `juggler prompt`
// uses via sendPrompt's dispatch. A nil cli (daemon unreachable at dial
// time) short-circuits straight to the daemon-unreachable error.
func callJugglerPromptTool(cli *rm.Client, params json.RawMessage) map[string]any {
	var call struct {
		Name      string `json:"name"`
		Arguments struct {
			Model     string `json:"model"`
			Task      string `json:"task"`
			MaxTokens int    `json:"max_tokens"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return mcpToolErr(fmt.Sprintf("invalid tools/call params: %v", err))
	}
	if call.Name != "juggler-prompt" {
		return mcpToolErr(fmt.Sprintf("unknown tool %q", call.Name))
	}
	if cli == nil {
		return mcpToolErr("juggler daemon unreachable — start it: juggler daemon")
	}
	maxTokens := call.Arguments.MaxTokens
	if maxTokens == 0 {
		maxTokens = mcpMaxTokensDefault
	}

	ctx, cancel := context.WithTimeout(context.Background(), mcpToolTimeout)
	defer cancel()

	resolved, err := cli.ResolveModel(ctx, rm.ResolveModelParams{Name: call.Arguments.Model})
	if err != nil {
		return mcpToolErr(fmt.Sprintf("resolve model %q: %v", call.Arguments.Model, err))
	}
	reply, err := sendPrompt(ctx, http.DefaultClient, resolved, call.Arguments.Model, call.Arguments.Task, maxTokens)
	if err != nil {
		return mcpToolErr(err.Error())
	}
	return mcpToolText(reply)
}

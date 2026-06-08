package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// runJobMCP is clown's built-in job-platform MCP server (RFC-0011): a
// hand-rolled, line-delimited JSON-RPC 2.0 server on stdin/stdout (the MCP
// stdio transport) exposing the seven job_* tools over internal/jobwake. It is
// not run by hand — clown injects it as a stdioServers entry in the synthesized
// clown-builtin-jobs plugin (jobmonitor.go), which clown-stdio-bridge wraps to
// streamable-HTTP and clown's own pluginhost manages (clown self-consumes
// RFC-0002). Every tool is equivalent to the matching `clown job` subcommand.
func runJobMCP(_ []string) int {
	serveJobMCP(os.Stdin, os.Stdout)
	return 0
}

// serveJobMCP runs the JSON-RPC loop against in/out, split from runJobMCP so
// tests can drive it with in-memory streams.
func serveJobMCP(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
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
			_ = enc.Encode(rpcResult(req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "clown-jobs", "version": "1"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}))
		case "tools/list":
			_ = enc.Encode(rpcResult(req.ID, map[string]any{"tools": jobToolList()}))
		case "tools/call":
			_ = enc.Encode(rpcResult(req.ID, callJobTool(req.Params)))
		case "notifications/initialized":
			// Notification (no id): no response.
		default:
			if len(req.ID) > 0 {
				_ = enc.Encode(rpcError(req.ID, -32601, fmt.Sprintf("unknown method %q", req.Method)))
			}
		}
	}
}

func rpcResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

// toolText wraps a successful tool result as MCP text content.
func toolText(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

// toolErr wraps a tool-level failure as MCP content with isError set, so the
// agent sees the message as tool output rather than a transport error.
func toolErr(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true}
}

// jobToolList is the static tool catalog (RFC-0011 §3). target is the channel
// override on every tool; defaults to the resolved session.
func jobToolList() []map[string]any {
	str := map[string]any{"type": "string"}
	target := map[string]any{"type": "string", "description": "channel override (session key, or '*' for broadcast where allowed); defaults to the resolved session"}
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	return []map[string]any{
		{"name": "job_start", "description": "Allocate a job and append its started record. Returns the job id.",
			"inputSchema": obj(map[string]any{"target": target, "label": str, "source": str})},
		{"name": "job_progress", "description": "Append a journal-only progress record (never wakes).",
			"inputSchema": obj(map[string]any{"job_id": str, "target": target, "message": str}, "job_id")},
		{"name": "job_done", "description": "Append the single terminal record and wake. state: succeeded|failed|cancelled|interrupted.",
			"inputSchema": obj(map[string]any{"job_id": str, "state": str, "target": target, "message": str, "result_ref": str}, "job_id", "state")},
		{"name": "job_message", "description": "Emit a standalone waking message job to a session ('*' broadcasts).",
			"inputSchema": obj(map[string]any{"target": target, "message": str, "from": str, "source": str, "result_ref": str}, "target", "message")},
		{"name": "job_read", "description": "Read a job's full record stream (job) or the channel's waking events (since/type filters). Returns a JSON array of records.",
			"inputSchema": obj(map[string]any{"job": str, "target": target, "since": str, "type": map[string]any{"type": "array", "items": str}})},
		{"name": "job_status", "description": "Journal+spool-derived status of a job (state, elapsed, last_activity, spool_bytes, tail). Returns a JSON object.",
			"inputSchema": obj(map[string]any{"job_id": str, "target": target, "tail": map[string]any{"type": "integer"}}, "job_id")},
		{"name": "job_spool_path", "description": "Resolve and return the absolute output-spool path for a job. Does not create the file.",
			"inputSchema": obj(map[string]any{"job_id": str, "target": target}, "job_id")},
	}
}

// callJobTool decodes a tools/call params object and dispatches to jobwake.
func callJobTool(params json.RawMessage) map[string]any {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolErr(fmt.Sprintf("invalid params: %v", err))
	}
	a := p.Arguments
	switch p.Name {
	case "job_start":
		id, err := jobwake.Start(jobwake.StartOpts{
			Target: argStr(a, "target"), Label: argStr(a, "label"), Source: argStr(a, "source")})
		return toolResult(id, err)
	case "job_progress":
		err := jobwake.Progress(argStr(a, "target"), argStr(a, "job_id"), argStr(a, "message"))
		return toolResult("ok", err)
	case "job_done":
		err := jobwake.Done(argStr(a, "target"), argStr(a, "job_id"),
			argStr(a, "state"), argStr(a, "message"), argStr(a, "result_ref"))
		return toolResult("ok", err)
	case "job_message":
		id, err := jobwake.Message(argStr(a, "target"), argStr(a, "source"),
			argStr(a, "from"), argStr(a, "message"), argStr(a, "result_ref"))
		return toolResult(id, err)
	case "job_read":
		return jobReadTool(a)
	case "job_status":
		return jobStatusTool(a)
	case "job_spool_path":
		path, err := jobwake.SpoolPath(argStr(a, "target"), argStr(a, "job_id"))
		return toolResult(path, err)
	default:
		return toolErr(fmt.Sprintf("unknown tool %q", p.Name))
	}
}

func toolResult(text string, err error) map[string]any {
	if err != nil {
		return toolErr(err.Error())
	}
	return toolText(text)
}

func jobReadTool(a map[string]any) map[string]any {
	target := argStr(a, "target")
	session := target
	if session == "" {
		session = jobwake.SessionKey()
	}
	cid := jobwake.ChannelID(session)

	var recs []jobwake.Record
	if job := argStr(a, "job"); job != "" {
		var err error
		recs, err = jobwake.ReadJob(cid, job)
		if err != nil {
			if os.IsNotExist(err) {
				recs = nil // unknown job => empty stream, not an error (RFC-0011 §3.5)
			} else {
				return toolErr(err.Error())
			}
		}
	} else {
		waking, err := jobwake.ScanWaking(cid)
		if err != nil {
			return toolErr(err.Error())
		}
		since := argStr(a, "since")
		typeSet := map[string]struct{}{}
		for _, t := range argStrSlice(a, "type") {
			typeSet[t] = struct{}{}
		}
		for _, r := range waking {
			if since != "" && r.TS <= since {
				continue
			}
			if len(typeSet) > 0 {
				if _, ok := typeSet[r.Type]; !ok {
					continue
				}
			}
			recs = append(recs, r)
		}
	}
	if recs == nil {
		recs = []jobwake.Record{}
	}
	b, err := json.Marshal(recs)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolText(string(b))
}

func jobStatusTool(a map[string]any) map[string]any {
	tail := argInt(a, "tail", 20)
	st, err := jobwake.StatusOf(argStr(a, "target"), argStr(a, "job_id"), tail, time.Now().UTC())
	if err != nil {
		return toolErr(err.Error())
	}
	b, err := json.Marshal(st)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolText(string(b))
}

// argStr extracts a string argument, returning "" when absent or not a string.
func argStr(a map[string]any, key string) string {
	if v, ok := a[key].(string); ok {
		return v
	}
	return ""
}

// argInt extracts an integer argument (JSON numbers decode as float64),
// returning def when absent or not a number.
func argInt(a map[string]any, key string, def int) int {
	switch v := a[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

// argStrSlice extracts a []string argument (JSON arrays decode as []any),
// skipping non-string elements.
func argStrSlice(a map[string]any, key string) []string {
	raw, ok := a[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Package jobmcp is the clown job-platform MCP server (RFC-0011): a hand-rolled,
// line-delimited JSON-RPC 2.0 server on stdin/stdout (the MCP stdio transport)
// exposing the job_* / chat_* tools over internal/jobwake. It is served by the
// `ringmaster mcp` and `troupe mcp` subcommands (RFC-0015 §6), which clown
// injects as stdioServers entries in the synthesized clown-builtin-jobs plugin
// (cmd/clown/jobmonitor.go); clown-stdio-bridge wraps it to streamable-HTTP and
// clown's own pluginhost manages it (clown self-consumes RFC-0002). Every tool
// is equivalent to the matching ringmaster/troupe CLI subcommand.
package jobmcp

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/amarbel-llc/clown/internal/jobwake"
)

// Tool surfaces (clown#144): the two intent-revealing halves of the platform.
// Ringmaster is job control (lifecycle + status); Troupe is messaging (chat +
// the standalone waking job_message). clown synthesizes one stdioServers entry
// per surface; an empty surface exposes the whole catalog (dev / conformance).
const (
	SurfaceRingmaster = "ringmaster"
	SurfaceTroupe     = "troupe"
)

// jobToolSurface classifies each tool into its split server (clown#144). It is
// the single source of truth for the partition: jobToolList filters the catalog
// against it, and the conformance tests assert the membership.
var jobToolSurface = map[string]string{
	"job_start":      SurfaceRingmaster,
	"job_progress":   SurfaceRingmaster,
	"job_done":       SurfaceRingmaster,
	"job_read":       SurfaceRingmaster,
	"job_status":     SurfaceRingmaster,
	"job_spool_path": SurfaceRingmaster,
	"job_wait":       SurfaceRingmaster,
	"job_message":    SurfaceTroupe,
	"chat_send":      SurfaceTroupe,
	"chat_read":      SurfaceTroupe,
	"chat_list":      SurfaceTroupe,
}

// jobServerName is the MCP serverInfo.name for a surface. Empty keeps the
// historical "clown-jobs" so the whole-catalog conformance path is unchanged.
func jobServerName(surface string) string {
	if surface == "" {
		return "clown-jobs"
	}
	return "clown-" + surface
}

// Serve runs the JSON-RPC loop against in/out. surface filters the advertised
// tool catalog (clown#144); tool DISPATCH is left whole — the catalog is the
// surface boundary the agent sees, and every job_* / chat_* op is the same
// harmless jobwake call regardless of which server routed it. surface "" exposes
// the whole platform (dev / conformance).
func Serve(in io.Reader, out io.Writer, surface string) {
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
				"serverInfo":      map[string]any{"name": jobServerName(surface), "version": "1"},
				"capabilities": map[string]any{
					"tools":   map[string]any{},
					"prompts": map[string]any{},
				},
			}))
		case "tools/list":
			_ = enc.Encode(rpcResult(req.ID, map[string]any{"tools": jobToolList(surface)}))
		case "tools/call":
			_ = enc.Encode(rpcResult(req.ID, callJobTool(req.Params)))
		case "prompts/list":
			_ = enc.Encode(rpcResult(req.ID, map[string]any{"prompts": jobPromptList()}))
		case "prompts/get":
			res, ok := jobPromptGet(req.Params)
			if !ok {
				_ = enc.Encode(rpcError(req.ID, -32602, "unknown prompt"))
				break
			}
			_ = enc.Encode(rpcResult(req.ID, res))
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
// override on every tool; defaults to the resolved session. surface filters the
// catalog to one split server's tools (clown#144); "" returns the whole platform.
func jobToolList(surface string) []map[string]any {
	str := map[string]any{"type": "string"}
	target := map[string]any{"type": "string", "description": "channel override (session key, or '*' for broadcast where allowed); defaults to the resolved session"}
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	resourceArr := map[string]any{
		"type":        "array",
		"description": "by-reference attachments (e.g. madder://blobs/<digest>); the receiver fetches each uri (clown#112)",
		"items": obj(map[string]any{
			"uri": str, "digest": str, "mediaType": str,
			"size": map[string]any{"type": "integer"},
		}, "uri"),
	}
	all := []map[string]any{
		{
			"name": "job_start", "description": "Allocate a job and append its started record. Returns the job id.",
			"inputSchema": obj(map[string]any{"target": target, "label": str, "source": str}),
		},
		{
			"name": "job_progress", "description": "Append a journal-only progress record (never wakes).",
			"inputSchema": obj(map[string]any{"job_id": str, "target": target, "message": str}, "job_id"),
		},
		{
			"name": "job_done", "description": "Append the single terminal record and wake. state: succeeded|failed|cancelled|interrupted.",
			"inputSchema": obj(map[string]any{"job_id": str, "state": str, "target": target, "message": str, "result_ref": str, "resources": resourceArr}, "job_id", "state"),
		},
		{
			"name": "job_message", "description": "Emit a standalone waking message job to a session ('*' broadcasts).",
			"inputSchema": obj(map[string]any{"target": target, "message": str, "from": str, "source": str, "result_ref": str, "resources": resourceArr}, "target", "message"),
		},
		{
			"name": "job_read", "description": "Read a job's full record stream (job) or the channel's waking events (since/type filters). Returns a JSON array of records.",
			"inputSchema": obj(map[string]any{"job": str, "target": target, "since": str, "type": map[string]any{"type": "array", "items": str}}),
		},
		{
			"name": "job_status", "description": "Journal+spool-derived status of a job (state, elapsed, last_activity, spool_bytes, tail). Returns a JSON object.",
			"inputSchema": obj(map[string]any{"job_id": str, "target": target, "tail": map[string]any{"type": "integer"}}, "job_id"),
		},
		{
			"name": "job_wait", "description": "Block until a job reaches a terminal state (succeeded|failed|cancelled|interrupted), then return its status JSON (same payload as job_status). Returns immediately if already terminal; errors if the job is unknown. This is a blocking JOIN: subject to the MCP request timeout for the job's remaining duration, so call it when you have nothing else to do — the launch + wake-on-completion flow stays the right choice when there's other work to interleave. timeout_sec (>0) bounds the wait.",
			"inputSchema": obj(map[string]any{"job_id": str, "target": target, "timeout_sec": map[string]any{"type": "integer"}, "tail": map[string]any{"type": "integer"}}, "job_id"),
		},
		{
			"name": "job_spool_path", "description": "Resolve and return the absolute output-spool path for a job. Does not create the file.",
			"inputSchema": obj(map[string]any{"job_id": str, "target": target}, "job_id"),
		},
		{
			"name": "chat_send", "description": "Send a chat message to another session (RFC-0013 §3). Write `message` like a git commit: a concise one-line summary, a blank line, then the details. clown delivers the summary as the wake notification the recipient sees immediately and the full text via chat_read. A single-line message (no body) is fine for something that fits in the summary; if you include details they MUST be separated from the summary by a blank line, or the call is rejected. target: session key / group-id group / '*' broadcast.",
			"inputSchema": obj(map[string]any{
				"target":  target,
				"message": map[string]any{"type": "string", "description": "git-commit format: a one-line summary, a blank line, then the body. The first line becomes the wake; the rest is the body recovered by chat_read."},
				"from":    str, "source": str, "resources": resourceArr,
			}, "target", "message"),
		},
		{
			"name": "chat_read", "description": "Read chat messages addressed to this session (own/group/broadcast) newer than the read cursor; advances the cursor unless peek. Returns a JSON array of {job,from,source,scope,subject,body,ts}.",
			"inputSchema": obj(map[string]any{"peek": map[string]any{"type": "boolean"}}),
		},
		{
			"name": "chat_list", "description": "List live chat recipients (presence): each {sessionKey, channelId, decoration, description, lastSeen}, groupable by decoration. Replaces spinclass chat-list-sessions.",
			"inputSchema": obj(map[string]any{}),
		},
	}
	if surface == "" {
		return all
	}
	out := make([]map[string]any, 0, len(all))
	for _, t := range all {
		if jobToolSurface[t["name"].(string)] == surface {
			out = append(out, t)
		}
	}
	return out
}

// jobSystemPromptName is the well-known MCP prompt the stdio bridge requests
// when clown asks for a dynamic system-prompt fragment (RFC-0002 §dynamic
// fragments). It MUST match childPromptName in cmd/clown-stdio-bridge.
const jobSystemPromptName = "system-prompt-append"

// jobPromptList advertises the single dynamic system-prompt the job platform
// contributes. Exposed via MCP prompts/list (a stable capability listing).
func jobPromptList() []map[string]any {
	return []map[string]any{{
		"name":        jobSystemPromptName,
		"description": "Live orientation for the clown job platform available in this session.",
	}}
}

// jobPromptGet answers an MCP prompts/get. ok is false for any name other than
// jobSystemPromptName, which the caller maps to a JSON-RPC error.
func jobPromptGet(params json.RawMessage) (map[string]any, bool) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, false
	}
	if p.Name != jobSystemPromptName {
		return nil, false
	}
	return map[string]any{
		"description": "clown job platform orientation",
		"messages": []map[string]any{{
			"role":    "user",
			"content": map[string]any{"type": "text", "text": jobSystemPromptFragment()},
		}},
	}, true
}

// jobFragmentTmplText is the embedded orientation-fragment template. The copy
// lives in jobfragment.tmpl so the markdown — backtick-dense code spans and all
// — is authored as markdown rather than as escaped Go string fragments (a raw
// string literal cannot contain backticks at all).
//
//go:embed jobfragment.tmpl
var jobFragmentTmplText string

// jobFragmentTmpl is parsed once at init: a malformed template panics here, at
// build/start, never at request time. The Session Identity section is gated on a
// non-empty .SessionKey inside the template, so it only renders when this clown
// has an address other sessions can reach — and this whole fragment is served
// only when the live job platform is up (CLOWN_DISABLE_JOB_WAKEUP and dev builds
// without the stdio bridge omit it), so it never advertises an unreachable one.
var jobFragmentTmpl = template.Must(template.New("jobfragment").Parse(jobFragmentTmplText))

// jobFragmentData is the template's view of the runtime-dynamic state: the
// per-instance session key (the troupe address — chat_send / job_message
// `target`, RFC-0013 §3) resolved from the identity clown injected into this
// process's env (clown#136), and the server's own live tool catalog. Neither is
// expressible by the build-time static fragment mechanism (FDR-0003).
type jobFragmentData struct {
	SessionKey string
	Tools      string
}

// jobSystemPromptFragment renders the orientation fragment at request time from
// the embedded template. A template execution error degrades to an empty
// fragment rather than failing the prompt fetch — best-effort, like the rest of
// the dynamic-fragment path.
func jobSystemPromptFragment() string {
	tools := jobToolList("") // orientation covers the whole platform, both surfaces
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if n, ok := t["name"].(string); ok {
			names = append(names, n)
		}
	}
	var b strings.Builder
	if err := jobFragmentTmpl.Execute(&b, jobFragmentData{
		SessionKey: jobwake.SessionKey(),
		Tools:      strings.Join(names, ", "),
	}); err != nil {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
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
			Target: argStr(a, "target"), Label: argStr(a, "label"), Source: argStr(a, "source"),
		})
		return toolResult(id, err)
	case "job_progress":
		err := jobwake.Progress(argStr(a, "target"), argStr(a, "job_id"), argStr(a, "message"))
		return toolResult("ok", err)
	case "job_done":
		err := jobwake.Done(argStr(a, "target"), argStr(a, "job_id"),
			argStr(a, "state"), argStr(a, "message"), argStr(a, "result_ref"), parseResources(a)...)
		return toolResult("ok", err)
	case "job_message":
		id, err := jobwake.Message(argStr(a, "target"), argStr(a, "source"),
			argStr(a, "from"), argStr(a, "message"), argStr(a, "result_ref"), parseResources(a)...)
		return toolResult(id, err)
	case "job_read":
		return jobReadTool(a)
	case "job_status":
		return jobStatusTool(a)
	case "job_wait":
		return jobWaitTool(a)
	case "job_spool_path":
		path, err := jobwake.SpoolPath(argStr(a, "target"), argStr(a, "job_id"))
		return toolResult(path, err)
	case "chat_send":
		from := argStr(a, "from")
		if from == "" {
			from = jobwake.SessionKey()
		}
		subject, body, err := jobwake.SplitMessage(argStr(a, "message"))
		if err != nil {
			return toolErr(err.Error())
		}
		id, err := jobwake.SendChat(argStr(a, "target"), from, argStr(a, "source"),
			subject, body, parseResources(a)...)
		return toolResult(id, err)
	case "chat_read":
		return chatReadTool(a)
	case "chat_list":
		return chatListTool()
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

func chatReadTool(a map[string]any) map[string]any {
	peek, _ := a["peek"].(bool)
	msgs, err := jobwake.ReadChat(peek)
	if err != nil {
		return toolErr(err.Error())
	}
	if msgs == nil {
		msgs = []jobwake.ChatMessage{}
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		return toolErr(err.Error())
	}
	return toolText(string(b))
}

// parseResources extracts the MCP `resources` argument — an array of
// {uri, digest, mediaType, size} objects — into []jobwake.Resource (clown#112).
// Entries without a uri are skipped; JSON numbers decode to float64.
func parseResources(a map[string]any) []jobwake.Resource {
	raw, ok := a["resources"].([]any)
	if !ok {
		return nil
	}
	var out []jobwake.Resource
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		uri := argStr(m, "uri")
		if uri == "" {
			continue
		}
		r := jobwake.Resource{URI: uri, Digest: argStr(m, "digest"), MediaType: argStr(m, "mediaType")}
		if s, ok := m["size"].(float64); ok {
			r.Size = int64(s)
		}
		out = append(out, r)
	}
	return out
}

func chatListTool() map[string]any {
	ps, err := jobwake.ListPresence(time.Now())
	if err != nil {
		return toolErr(err.Error())
	}
	if ps == nil {
		ps = []jobwake.Presence{}
	}
	b, err := json.Marshal(ps)
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

// jobWaitTool blocks until the job is terminal, then returns its status JSON —
// the same payload as job_status, so a caller joins and reads the result in one
// tool call (clown#154). timeout_sec (>0) bounds the wait; on expiry the job is
// still running and the tool reports an error rather than a status. The call
// blocks the single-threaded JSON-RPC loop for its duration, which is the
// documented blocking-join contract (subject to the MCP request timeout).
func jobWaitTool(a map[string]any) map[string]any {
	ctx := context.Background()
	if secs := argInt(a, "timeout_sec", 0); secs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(secs)*time.Second)
		defer cancel()
	}
	target, jobID := argStr(a, "target"), argStr(a, "job_id")
	if _, err := jobwake.WaitDone(ctx, target, jobID); err != nil {
		return toolErr(err.Error())
	}
	tail := argInt(a, "tail", 20)
	st, err := jobwake.StatusOf(target, jobID, tail, time.Now().UTC())
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

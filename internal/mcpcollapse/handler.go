package mcpcollapse

// handler.go implements the collapse itself: the piece that satisfies
// mcphttp.RequestHandler so it plugs into the mcphttp Server spine, and whose
// tools/list returns EXACTLY three generic verbs (mcp_list, mcp_describe,
// mcp_call) instead of the N upstream tools. tools/call branches on the verb
// name and dispatches:
//
//   - mcp_list     — an ultra-lean id+description listing (no schemas), grouped
//     by server, plus the degraded-upstream roster, so the agent can discover
//     what is callable without paying the context cost of every schema.
//   - mcp_describe — the stored raw inputSchema + description for one tool_id,
//     handed back verbatim so the agent can construct a valid call.
//   - mcp_call     — validate args against the stored schema, then dispatch a
//     real tools/call to the owning upstream and return its result verbatim.
//
// Only aggregator-ORIGINATED failures (unknown verb, unknown tool_id, schema
// validation failure, upstream transport error) become a shaped MCP error
// result (isError:true with a self-correction hint). A tool that RAN and
// returned an error result passes through untouched — the aggregator never
// re-wraps a downstream tool's own outcome.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"code.linenisgreat.com/clown/internal/mcphttp"
)

// maxUpstreamCallBytes bounds a single upstream tools/call response so a
// misbehaving upstream cannot balloon memory during a dispatch. Matches the
// enumeration-time bound in aggregator.go.
const maxUpstreamCallBytes = 1 << 20

// Handler is the collapse RequestHandler: it fronts an Aggregator's registry and
// answers the harness's JSON-RPC with the three generic verbs. It is safe for
// concurrent use — the registry is immutable after the fan-out, and the only
// mutable state (the per-upstream session-id cache) is mutex-guarded.
//
// It satisfies mcphttp.RequestHandler. SendRequest carries the whole collapse;
// SendNotification and Subscribe are inert because this handler originates no
// server-initiated traffic (no progress FORWARDING back to the harness — that
// is a deferred followup) and needs no client notifications.
type Handler struct {
	agg *Aggregator

	// mu guards sessions, the lazily-populated per-upstream session-id cache
	// (see sessionForURL). The registry itself needs no lock — it is immutable
	// after NewAggregator returns.
	mu sync.Mutex
	// sessions caches the Mcp-Session-Id captured from each upstream's initialize
	// handshake, keyed by upstream URL, so repeated mcp_call dispatches to one
	// upstream reuse a single session rather than re-initializing per call. The
	// startup fan-out (NewAggregator) does not retain the session id it used for
	// enumeration, so this handler establishes and caches its own lazily on the
	// first mcp_call to each upstream.
	sessions map[string]string
}

// NewHandler wraps an Aggregator as the collapse RequestHandler.
func NewHandler(agg *Aggregator) *Handler {
	return &Handler{agg: agg, sessions: make(map[string]string)}
}

// SendRequest answers one JSON-RPC request. tools/list returns the three verbs;
// tools/call dispatches the named verb; initialize is answered locally (the
// harness handshake); every other method is a JSON-RPC method-not-found. The
// response is always a full JSON-RPC envelope keyed to the request id.
func (h *Handler) SendRequest(ctx context.Context, idKey string, body []byte) (json.RawMessage, error) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("mcpcollapse: parsing request: %w", err)
	}

	switch req.Method {
	case "initialize":
		return h.initializeResult(req.ID), nil
	case "tools/list":
		return h.toolsListResult(req.ID)
	case "tools/call":
		return h.toolsCallResult(ctx, req.ID, req.Params)
	default:
		return methodNotFound(req.ID, req.Method), nil
	}
}

// SendNotification is a no-op: the collapse originates no client→upstream
// notifications, and forwarding harness notifications to N upstreams is out of
// scope. It never saturates, so it never returns mcphttp.ErrQueueFull.
func (h *Handler) SendNotification(body []byte) error { return nil }

// Subscribe returns a closed channel and a no-op cancel: the collapse emits no
// server-initiated messages to the harness (progress forwarding back to the
// harness is a deferred followup), so the GET SSE stream has nothing to carry.
func (h *Handler) Subscribe() (<-chan json.RawMessage, func()) {
	ch := make(chan json.RawMessage)
	close(ch)
	return ch, func() {}
}

// initializeResult answers the harness's initialize with a minimal server
// capability advertisement — tools only, since the collapse exposes no
// resources or prompts.
func (h *Handler) initializeResult(id json.RawMessage) json.RawMessage {
	return jsonRPCResult(id, json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"clown-mcp-collapse","version":"1"}}`))
}

// toolsListResult builds the tools/list response carrying exactly the three
// verbs. This is the collapse: the harness sees three tools regardless of how
// many the fronted upstreams expose.
func (h *Handler) toolsListResult(id json.RawMessage) (json.RawMessage, error) {
	result := map[string]any{"tools": collapseVerbs()}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("mcpcollapse: encoding tools/list: %w", err)
	}
	return jsonRPCResult(id, encoded), nil
}

// collapseVerbs returns the three generic tool definitions. Each carries a
// strongly-shaped description telling the agent the id scheme and the two-step
// discover→describe→call flow, plus an inputSchema constraining its arguments.
func collapseVerbs() []map[string]any {
	return []map[string]any{
		{
			"name": "mcp_list",
			"description": "List the tools available across all fronted MCP servers as compact " +
				"{tool_id: description} rows grouped by server (NO input schemas — call " +
				"mcp_describe for a tool's schema). tool_id is the dotted \"{server}.{tool}\" " +
				"id you pass to mcp_describe and mcp_call. Optional filters: query (substring " +
				"match against id or description) and server (exact server name). The listing " +
				"also names any upstream servers that failed to enumerate.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":  map[string]any{"type": "string", "description": "substring filter against tool id or description"},
					"server": map[string]any{"type": "string", "description": "exact server-name filter"},
				},
			},
		},
		{
			"name": "mcp_describe",
			"description": "Return the full input schema and description for one tool, so you can " +
				"construct a valid mcp_call. tool_id is the dotted \"{server}.{tool}\" id from " +
				"mcp_list (e.g. \"grit.commit\").",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tool_id": map[string]any{"type": "string", "description": "the dotted {server}.{tool} id from mcp_list"},
				},
				"required": []string{"tool_id"},
			},
		},
		{
			"name": "mcp_call",
			"description": "Invoke one fronted tool. tool_id is the dotted \"{server}.{tool}\" id " +
				"from mcp_list; args is the object matching that tool's input schema (see " +
				"mcp_describe). args is validated against the schema before dispatch — a " +
				"missing required field or wrong-typed field is rejected with an explanation " +
				"instead of being sent upstream. The fronted tool's own result is returned " +
				"verbatim.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tool_id": map[string]any{"type": "string", "description": "the dotted {server}.{tool} id from mcp_list"},
					"args":    map[string]any{"type": "object", "description": "arguments object matching the tool's input schema"},
				},
				"required": []string{"tool_id", "args"},
			},
		},
	}
}

// toolsCallResult parses the tools/call params (name + arguments) and branches
// on the verb name. An unrecognized verb is a JSON-RPC method-not-found (the
// harness asked for a tool that does not exist), distinct from the shaped
// isError results the verbs themselves return for bad arguments.
func (h *Handler) toolsCallResult(ctx context.Context, id, params json.RawMessage) (json.RawMessage, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return errorResult(id, "invalid tools/call params: "+err.Error()), nil
	}

	switch call.Name {
	case "mcp_list":
		return h.verbList(id, call.Arguments)
	case "mcp_describe":
		return h.verbDescribe(id, call.Arguments)
	case "mcp_call":
		return h.verbCall(ctx, id, call.Arguments)
	default:
		return methodNotFound(id, "tools/call name "+call.Name), nil
	}
}

// verbList renders the ultra-lean listing: one "{tool_id} — {description}" row
// per registered tool, grouped under a per-server header, honoring the optional
// query (substring) and server (exact) filters, followed by the degraded-upstream
// roster and any non-fatal warnings. Only id and description are emitted — no
// schemas — which is the context savings the collapse exists for.
func (h *Handler) verbList(id, arguments json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Query  string `json:"query"`
		Server string `json:"server"`
	}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return errorResult(id, "invalid mcp_list arguments: "+err.Error()), nil
		}
	}
	query := strings.ToLower(args.Query)

	var sb strings.Builder
	var currentServer string
	var matched int
	for _, e := range h.agg.Registry().Entries() {
		if args.Server != "" && e.Server != args.Server {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(e.ID()), query) &&
			!strings.Contains(strings.ToLower(e.Description), query) {
			continue
		}
		if e.Server != currentServer {
			if currentServer != "" {
				sb.WriteString("\n")
			}
			fmt.Fprintf(&sb, "%s:\n", e.Server)
			currentServer = e.Server
		}
		fmt.Fprintf(&sb, "  %s — %s\n", e.ID(), e.Description)
		matched++
	}
	if matched == 0 {
		sb.WriteString("(no tools match the given filters)\n")
	}

	if degraded := h.agg.Degraded(); len(degraded) > 0 {
		sb.WriteString("\ndegraded upstreams (failed to enumerate, tools unavailable):\n")
		for _, d := range degraded {
			reason := "unknown"
			if d.Err != nil {
				reason = d.Err.Error()
			}
			fmt.Fprintf(&sb, "  %s (%s) — %s\n", d.Name, d.URL, reason)
		}
	}
	if warnings := h.agg.Warnings(); len(warnings) > 0 {
		sb.WriteString("\nwarnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(&sb, "  %s\n", w)
		}
	}

	return textResult(id, sb.String(), false), nil
}

// verbDescribe resolves tool_id and returns its stored raw inputSchema plus
// description, verbatim, so the agent can construct a valid mcp_call. An unknown
// tool_id is a shaped isError result naming the miss.
func (h *Handler) verbDescribe(id, arguments json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ToolID string `json:"tool_id"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return errorResult(id, "invalid mcp_describe arguments: "+err.Error()), nil
	}
	if args.ToolID == "" {
		return errorResult(id, "mcp_describe requires a non-empty tool_id"), nil
	}

	entry, ok := h.agg.Registry().Lookup(args.ToolID)
	if !ok {
		return errorResult(id, fmt.Sprintf("unknown tool_id %q — call mcp_list to see valid ids", args.ToolID)), nil
	}

	schema := entry.Schema
	if len(schema) == 0 {
		schema = json.RawMessage(`{}`)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n\n%s\n\ninputSchema:\n%s\n", entry.ID(), entry.Description, string(schema))
	return textResult(id, sb.String(), false), nil
}

// verbCall resolves tool_id, validates args against the stored schema, and — only
// when valid — dispatches a real tools/call to the owning upstream and returns
// its result verbatim. Unknown tool_id and validation failure are shaped isError
// results and DO NOT dispatch upstream. A transport error on the dispatch is a
// shaped isError result too; a tool that ran and returned its own (error or
// success) result passes through untouched.
func (h *Handler) verbCall(ctx context.Context, id, arguments json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ToolID string          `json:"tool_id"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return errorResult(id, "invalid mcp_call arguments: "+err.Error()), nil
	}
	if args.ToolID == "" {
		return errorResult(id, "mcp_call requires a non-empty tool_id"), nil
	}

	entry, ok := h.agg.Registry().Lookup(args.ToolID)
	if !ok {
		return errorResult(id, fmt.Sprintf("unknown tool_id %q — call mcp_list to see valid ids", args.ToolID)), nil
	}

	callArgs := args.Args
	if len(callArgs) == 0 {
		callArgs = json.RawMessage(`{}`)
	}
	if err := validateArgs(entry.Schema, callArgs); err != nil {
		return errorResult(id, fmt.Sprintf("args do not satisfy the schema for %q: %v — call mcp_describe for the schema", args.ToolID, err)), nil
	}

	result, err := h.dispatchUpstream(ctx, entry, callArgs)
	if err != nil {
		return errorResult(id, fmt.Sprintf("dispatching %q to upstream %s: %v", args.ToolID, entry.Server, err)), nil
	}
	// The upstream's result — success OR the tool's own isError — passes through
	// verbatim; the aggregator never re-wraps a downstream tool's outcome.
	return jsonRPCResult(id, result), nil
}

// dispatchUpstream sends a real tools/call to entry.URL naming the real upstream
// Tool and the validated args, and returns the upstream's JSON-RPC result
// verbatim (the CallToolResult object). It reuses the cached session id for the
// upstream (establishing one lazily on first call), and carries a
// params._meta.progressToken so heartbeat-on-progress upstreams keep their
// stream warm. A JSON-RPC error envelope from the upstream is surfaced as an
// error for verbCall to shape.
func (h *Handler) dispatchUpstream(ctx context.Context, entry Entry, args json.RawMessage) (json.RawMessage, error) {
	sessionID, err := h.sessionForURL(ctx, entry.URL)
	if err != nil {
		return nil, fmt.Errorf("establishing session: %w", err)
	}

	callBody := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":"mcp-collapse-call","method":"tools/call","params":{"name":%s,"arguments":%s,"_meta":{"progressToken":"mcp-collapse-call"}}}`,
		jsonQuote(entry.Tool), string(args),
	)

	respBody, newSessionID, err := mcphttp.PostJSONRPC(ctx, entry.URL, sessionID, callBody, maxUpstreamCallBytes)
	if err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}
	// Some upstreams rotate the session id on each response; keep the cache fresh.
	if newSessionID != "" && newSessionID != sessionID {
		h.mu.Lock()
		h.sessions[entry.URL] = newSessionID
		h.mu.Unlock()
	}

	var parsed struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parsing tools/call response: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("upstream error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if parsed.Result == nil {
		return nil, fmt.Errorf("tools/call response has neither result nor error")
	}
	return parsed.Result, nil
}

// sessionForURL returns the cached Mcp-Session-Id for an upstream URL,
// establishing one lazily via an initialize handshake on first use. The startup
// fan-out does not retain the session it used to enumerate, so the first mcp_call
// to each upstream re-initializes here and caches the result for reuse. An
// upstream that implements no session continuity hands back an empty id, which is
// cached and passed through as "no header" — correct for such upstreams.
func (h *Handler) sessionForURL(ctx context.Context, url string) (string, error) {
	h.mu.Lock()
	cached, ok := h.sessions[url]
	h.mu.Unlock()
	if ok {
		return cached, nil
	}

	_, sessionID, err := mcphttp.PostJSONRPC(ctx, url, "", `{"jsonrpc":"2.0","id":"mcp-collapse-call-init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"clown-mcp-collapse","version":"1"}}}`, maxUpstreamCallBytes)
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}

	h.mu.Lock()
	h.sessions[url] = sessionID
	h.mu.Unlock()
	return sessionID, nil
}

// jsonQuote returns s as a JSON string literal (quoted, escaped). Used to inline
// a tool name into a hand-built JSON-RPC body without a struct round-trip.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- JSON-RPC / MCP result shaping helpers ---

// jsonRPCResult wraps a raw result object in a JSON-RPC 2.0 response envelope
// keyed to id.
func jsonRPCResult(id json.RawMessage, result json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, string(id), string(result)))
}

// methodNotFound builds a JSON-RPC method-not-found (-32601) error envelope. Used
// for methods and verb names the collapse does not implement — a protocol-level
// miss, distinct from the isError tool results the verbs return.
func methodNotFound(id json.RawMessage, what string) json.RawMessage {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	msg, _ := json.Marshal("mcpcollapse: method not found: " + what)
	return json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":%s}}`, string(id), string(msg)))
}

// textResult builds an MCP CallToolResult with a single text content block,
// wrapped in a JSON-RPC result envelope. isError marks an aggregator-originated
// failure so the agent can self-correct.
func textResult(id json.RawMessage, text string, isError bool) json.RawMessage {
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
	encoded, _ := json.Marshal(result)
	return jsonRPCResult(id, encoded)
}

// errorResult is a shaped aggregator-originated failure: an MCP CallToolResult
// with isError:true carrying the message, returned as a normal JSON-RPC result
// (not a JSON-RPC error) so the harness surfaces it to the agent as a tool
// outcome it can read and correct from.
func errorResult(id json.RawMessage, message string) json.RawMessage {
	return textResult(id, "mcp-collapse error: "+message, true)
}

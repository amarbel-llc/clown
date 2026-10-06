// Package jugglertools is the tool-plane client for `juggler run` (FDR 0019
// §5): an MCP client for exactly ONE upstream (a moxy instance) over
// streamable HTTP. It does initialize, tools/list and tools/call, reusing
// internal/mcphttp for the transport and SSE framing.
//
// Error taxonomy. Call distinguishes two failure planes:
//
//   - the returned error is a transport or protocol failure (network error,
//     non-200, malformed JSON-RPC, JSON-RPC error envelope other than
//     invalid params, oversized response). The agent loop treats it as a
//     failed call it did not get a tool answer for.
//   - isError == true is MCP's tool-level failure: the tool ran (or moxy
//     answered on its behalf) and reported failure in the CallToolResult.
//     The model should see the content as ordinary tool output. A JSON-RPC
//     invalid-params error (-32602) is reported this way too, with the
//     error message as the content.
//
// Permission denial. Moxy's headless posture (any non-always-allow tier
// resolves to deny for a juggler agent) is moxy's responsibility, not this
// package's. This client assumes a denial arrives as an isError tool result
// and treats it exactly like any other tool-level error; it does not
// inspect or special-case it.
package jugglertools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"code.linenisgreat.com/clown/internal/jugglerloop"
	"code.linenisgreat.com/clown/internal/mcphttp"
)

// Client satisfies the loop's executor directly; its error taxonomy is the
// loop's (a returned error ends the run with tool_error, isError is a
// tool-level failure the model sees).
var _ jugglerloop.ToolExecutor = (*Client)(nil)

const (
	// MaxResponseBytes bounds one upstream response body. Matches
	// mcpcollapse's maxEnumerateBytes / maxUpstreamCallBytes (1 MiB).
	MaxResponseBytes = 1 << 20

	// DefaultCallTimeout bounds each round-trip (initialize, tools/list,
	// tools/call) unless overridden with WithCallTimeout.
	DefaultCallTimeout = 30 * time.Second

	protocolVersion = "2025-06-18"
)

// ErrResponseTooLarge is returned when an upstream response exceeds the
// client's byte cap.
var ErrResponseTooLarge = errors.New("jugglertools: upstream response exceeds byte cap")

// ToolSpec is one tool as advertised by the upstream: the loop's own spec,
// with Kind extracted by ExtractKind ("" when undeclared).
type ToolSpec = jugglerloop.ToolSpec

// Option configures a Client.
type Option func(*Client)

// WithCallTimeout sets the per-request timeout (non-positive keeps the
// default).
func WithCallTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithMaxResponseBytes sets the response byte cap (non-positive keeps the
// default).
func WithMaxResponseBytes(n int64) Option {
	return func(c *Client) {
		if n > 0 {
			c.maxBytes = n
		}
	}
}

// Client talks to one MCP server. Safe for concurrent use.
type Client struct {
	url      string
	timeout  time.Duration
	maxBytes int64

	mu        sync.Mutex
	sessionID string
	nextID    int
}

// New returns a Client for the MCP endpoint at url.
func New(url string, opts ...Option) *Client {
	c := &Client{url: url, timeout: DefaultCallTimeout, maxBytes: MaxResponseBytes}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Initialize performs the MCP initialize handshake and captures the
// Mcp-Session-Id the server may require echoed on later calls.
func (c *Client) Initialize(ctx context.Context) error {
	params := `{"protocolVersion":"` + protocolVersion + `","capabilities":{},"clientInfo":{"name":"juggler-run","version":"1"}}`
	// The session id must not be sent on initialize itself.
	c.mu.Lock()
	c.sessionID = ""
	c.mu.Unlock()
	_, err := c.rpc(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return nil
}

// ListTools returns the upstream's tools. Entries with an empty name are
// skipped.
func (c *Client) ListTools(ctx context.Context) ([]ToolSpec, error) {
	result, err := c.rpc(ctx, "tools/list", `{}`)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	var parsed struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Meta        json.RawMessage `json:"_meta"`
			Annotations json.RawMessage `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, fmt.Errorf("parsing tools/list result: %w", err)
	}
	specs := make([]ToolSpec, 0, len(parsed.Tools))
	for _, t := range parsed.Tools {
		if t.Name == "" {
			continue
		}
		specs = append(specs, ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Kind:        ExtractKind(t.Meta, t.Annotations),
		})
	}
	return specs, nil
}

// ExtractKind is the single place the artifact-kind convention lives
// (FDR 0019 §4: kind comes from the tool server's own metadata). Rule: the
// string value of "kind" in the tool's `_meta` object if present and
// non-empty, else the string value of "kind" in its `annotations` object,
// else "". Either argument may be nil/empty.
func ExtractKind(meta, annotations json.RawMessage) string {
	for _, raw := range []json.RawMessage{meta, annotations} {
		if len(raw) == 0 {
			continue
		}
		var m struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Kind != "" {
			return m.Kind
		}
	}
	return ""
}

// Call invokes one tool (tools/call). content is the CallToolResult's raw
// "content" array (nil if absent) — or, when the result also carries
// "structuredContent" (MCP's typed result), the object
// {"content": <array>, "structuredContent": <value>}, so the typed result
// reaches the loop's URI extractor and the model alike. isError is the
// tool-level isError flag; err is a transport or protocol failure only.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, bool, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	nameJSON, err := json.Marshal(name)
	if err != nil {
		return nil, false, err
	}
	params := fmt.Sprintf(`{"name":%s,"arguments":%s}`, nameJSON, args)
	if !json.Valid([]byte(params)) {
		return nil, false, fmt.Errorf("tools/call %q: arguments are not valid JSON", name)
	}
	result, err := c.rpc(ctx, "tools/call", params)
	if err != nil {
		// Invalid params means the model's arguments were unusable: the model
		// can fix that, so it is a tool-level error, not a transport failure.
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) && rpcErr.Code == codeInvalidParams {
			content, _ := json.Marshal([]map[string]string{{"type": "text", "text": rpcErr.Message}})
			return content, true, nil
		}
		return nil, false, fmt.Errorf("tools/call %q: %w", name, err)
	}
	var parsed struct {
		Content           json.RawMessage `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, false, fmt.Errorf("tools/call %q: parsing result: %w", name, err)
	}
	if len(parsed.StructuredContent) == 0 || string(parsed.StructuredContent) == "null" {
		return parsed.Content, parsed.IsError, nil
	}
	content := parsed.Content
	if len(content) == 0 {
		content = json.RawMessage(`[]`)
	}
	both, err := json.Marshal(struct {
		Content           json.RawMessage `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}{content, parsed.StructuredContent})
	if err != nil {
		return nil, false, fmt.Errorf("tools/call %q: %w", name, err)
	}
	return both, parsed.IsError, nil
}

// codeInvalidParams is JSON-RPC's "invalid params" error code.
const codeInvalidParams = -32602

// rpcError is a JSON-RPC error envelope.
type rpcError struct {
	Code    int
	Message string
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("upstream error %d: %s", e.Code, e.Message)
}

// rpc sends one JSON-RPC request and returns the "result" member. A
// JSON-RPC error envelope, a missing result, an oversized body, or a
// transport failure is returned as an error.
func (c *Client) rpc(ctx context.Context, method, params string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	session := c.sessionID
	c.mu.Unlock()

	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)
	// Read one byte past the cap so truncation is detectable: PostJSONRPC
	// silently truncates at its limit.
	raw, newSession, err := mcphttp.PostJSONRPC(ctx, c.url, session, body, c.maxBytes+1)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > c.maxBytes {
		return nil, ErrResponseTooLarge
	}
	if newSession != "" && newSession != session {
		c.mu.Lock()
		c.sessionID = newSession
		c.mu.Unlock()
	}

	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parsing JSON-RPC response: %w", err)
	}
	if env.Error != nil {
		return nil, &rpcError{Code: env.Error.Code, Message: env.Error.Message}
	}
	if env.Result == nil {
		return nil, errors.New("response has neither result nor error")
	}
	return env.Result, nil
}

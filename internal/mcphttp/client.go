package mcphttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MCPSessionIDHeader is the MCP streamable-HTTP transport's session
// continuity header. moxy's native httpServers implementation
// (internal/streamhttp, amarbel-llc/moxy) requires it on every request
// after initialize — the initialize response carries the session id to
// echo back on subsequent calls (tools/list here). clown-stdio-bridge
// doesn't require it (its handlePost has no session concept), so always
// forwarding it when present is harmless there too.
const MCPSessionIDHeader = "Mcp-Session-Id"

// PostJSONRPC POSTs a single JSON-RPC request body to url and returns the
// extracted JSON-RPC message body plus the response's Mcp-Session-Id header
// (empty if absent), reading at most maxBytes of the response body. A non-200
// status is treated as an error. sessionID, when non-empty, is echoed back on
// the request via MCPSessionIDHeader (see pluginhost.Host.FetchToolCatalog).
//
// The MCP streamable-HTTP transport lets a server answer a POST with either
// a plain application/json body or a text/event-stream response framing
// the same JSON-RPC message as one or more "data: <json>\n\n" events
// (clown-stdio-bridge defaults to the latter — see heartbeatMode in
// cmd/clown-stdio-bridge/http.go, which streams unless
// CLOWN_BRIDGE_HEARTBEAT_INTERVAL=0 is set on that server). Response
// Content-Type decides which framing to parse; clown doesn't control that
// env var per-fetch, so both must be handled here rather than assuming
// plain JSON.
func PostJSONRPC(ctx context.Context, url, sessionID, body string, maxBytes int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set(MCPSessionIDHeader, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	respSessionID := resp.Header.Get(MCPSessionIDHeader)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, "", err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		data, err := ExtractSSEData(raw)
		return data, respSessionID, err
	}
	return raw, respSessionID, nil
}

// ExtractSSEData returns the payload of the last "data: <payload>" event
// in an SSE stream body. handlePostStreaming (cmd/clown-stdio-bridge)
// emits zero or more heartbeat/progress events before exactly one final
// event carrying the actual JSON-RPC response or error — the last "data:"
// line is always that final message.
func ExtractSSEData(raw []byte) ([]byte, error) {
	var last []byte
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			last = []byte(data)
		} else if data, ok := strings.CutPrefix(line, "data:"); ok {
			last = []byte(strings.TrimPrefix(data, " "))
		}
	}
	if last == nil {
		return nil, fmt.Errorf("no data: event found in SSE response")
	}
	return last, nil
}

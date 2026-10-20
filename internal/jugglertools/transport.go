package jugglertools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The streamable-HTTP transport: juggler's own copy of the part of clown's
// internal/mcphttp the client uses, so juggler's packages import nothing from
// clown (clown#258).

// mcpSessionIDHeader is the MCP streamable-HTTP session continuity header.
// moxy requires it on every request after initialize; the initialize
// response carries the id to echo back.
const mcpSessionIDHeader = "Mcp-Session-Id"

// postJSONRPC POSTs one JSON-RPC request body to url and returns the
// JSON-RPC message body plus the response's Mcp-Session-Id (empty if
// absent). maxBytes (> 0) bounds the response read; a non-200 status is an
// error. sessionID, when non-empty, is sent as mcpSessionIDHeader.
//
// A server may answer with plain application/json or with text/event-stream
// framing the same message as one or more "data: <json>" events; the
// response Content-Type decides which is parsed.
func postJSONRPC(ctx context.Context, url, sessionID, body string, maxBytes int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set(mcpSessionIDHeader, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	respSessionID := resp.Header.Get(mcpSessionIDHeader)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, "", err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		data, err := extractSSEData(raw)
		return data, respSessionID, err
	}
	return raw, respSessionID, nil
}

// extractSSEData returns the payload of the last "data: <payload>" event in
// an SSE body: heartbeat or progress events may precede exactly one final
// event carrying the JSON-RPC response.
func extractSSEData(raw []byte) ([]byte, error) {
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

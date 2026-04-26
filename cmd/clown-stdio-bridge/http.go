package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// httpHandler exposes the wrapped stdio MCP server over streamable-HTTP
// per https://modelcontextprotocol.io/specification/2025-06-18/basic/transports.
//
// v1 simplifications (per FDR 0002 §"Concurrency"):
//   - POST /mcp request → application/json response (no SSE-on-POST).
//   - GET /mcp → SSE stream of server-initiated messages.
//   - DELETE /mcp → 405 Method Not Allowed (no session termination).
//   - Origin restricted to loopback, matching the transport spec's
//     security warning.
type httpHandler struct {
	t      *translator
	logger logger
}

// jsonRPCError mirrors the JSON-RPC 2.0 error response shape.
type jsonRPCError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Error   jsonRPCErrorObj `json:"error"`
}

type jsonRPCErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP error codes (subset). -32000 is the JSON-RPC reserved-for-server
// range; we use one slot for queue-full back-pressure signaling.
const (
	codeBridgeQueueFull = -32000
	codeParseError      = -32700
	codeInvalidRequest  = -32600
)

func (h *httpHandler) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !validateOrigin(r) {
		http.Error(w, "origin not permitted", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodDelete:
		http.Error(w, "session termination not supported by clown-stdio-bridge", http.StatusMethodNotAllowed)
	default:
		w.Header().Set("Allow", "POST, GET, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *httpHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		writeJSONRPCError(w, http.StatusBadRequest, nil, codeParseError,
			"invalid JSON-RPC body: "+err.Error())
		return
	}

	hasID := len(probe.ID) > 0 && string(probe.ID) != "null"
	hasMethod := probe.Method != ""

	switch {
	case hasMethod && hasID:
		// Request — forward and wait for matching response.
		idKey := string(probe.ID)
		resp, err := h.t.SendRequest(r.Context(), idKey, body)
		if err != nil {
			if err == ErrQueueFull {
				writeJSONRPCError(w, http.StatusServiceUnavailable, probe.ID,
					codeBridgeQueueFull,
					"clown-stdio-bridge: inbound queue saturated")
				return
			}
			if err == r.Context().Err() {
				// Client disconnected; nothing useful to send.
				return
			}
			writeJSONRPCError(w, http.StatusInternalServerError, probe.ID,
				codeInvalidRequest,
				"clown-stdio-bridge: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	case hasMethod && !hasID:
		// Notification — fire-and-forget.
		if err := h.t.SendNotification(body); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	case !hasMethod && hasID:
		// Response from client (e.g., to a server-initiated request).
		// Forward fire-and-forget; the wrapped server will route by id.
		if err := h.t.SendNotification(body); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		writeJSONRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest,
			"JSON-RPC body has neither method nor id")
	}
}

func (h *httpHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sub, cancel := h.t.Subscribe()
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-sub:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// validateOrigin enforces the loopback-only posture mandated by the
// streamable-HTTP spec's security warning. Empty Origin is permitted
// (curl, programmatic clients without a browser context).
func validateOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	// Reject any other origin (DNS-rebinding mitigation).
	return false
}

func writeJSONRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonRPCError{
		JSONRPC: "2.0",
		ID:      id,
		Error:   jsonRPCErrorObj{Code: code, Message: msg},
	})
}


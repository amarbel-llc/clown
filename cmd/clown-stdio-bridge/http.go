package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// heartbeatEnvVar selects the cadence at which handlePost emits keep-alive
// activity on streaming responses. Unset uses heartbeatDefault. "0" or
// "off" disables heartbeats AND falls back to plain application/json
// responses (legacy behavior). Any other value is parsed by
// time.ParseDuration.
const heartbeatEnvVar = "CLOWN_BRIDGE_HEARTBEAT_INTERVAL"

const heartbeatDefault = 30 * time.Second

// heartbeatInterval reports the configured heartbeat cadence. Returns 0
// when heartbeats are disabled.
func heartbeatInterval() time.Duration {
	v, set := os.LookupEnv(heartbeatEnvVar)
	if !set {
		return heartbeatDefault
	}
	switch v {
	case "0", "off", "":
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return heartbeatDefault
	}
	return d
}

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
		idKey := string(probe.ID)
		hasToken := len(extractProgressToken(body)) > 0
		started := time.Now()
		h.logger.Printf(
			"clown-stdio-bridge: post start id=%s method=%q has_progressToken=%t body_size=%d",
			idKey, probe.Method, hasToken, len(body))
		if interval := heartbeatInterval(); interval > 0 {
			h.handlePostStreaming(w, r, idKey, probe.ID, body, interval, started)
			return
		}
		// Synchronous JSON response (heartbeats disabled).
		resp, err := h.t.SendRequest(r.Context(), idKey, body)
		if err != nil {
			if errors.Is(err, ErrQueueFull) {
				h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=queue_full elapsed_ms=%d",
					idKey, time.Since(started).Milliseconds())
				writeJSONRPCError(w, http.StatusServiceUnavailable, probe.ID,
					codeBridgeQueueFull,
					"clown-stdio-bridge: inbound queue saturated")
				return
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Client disconnected; nothing useful to send.
				h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=ctx_canceled elapsed_ms=%d",
					idKey, time.Since(started).Milliseconds())
				return
			}
			h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=error elapsed_ms=%d err=%q",
				idKey, time.Since(started).Milliseconds(), err.Error())
			writeJSONRPCError(w, http.StatusInternalServerError, probe.ID,
				codeInvalidRequest,
				"clown-stdio-bridge: "+err.Error())
			return
		}
		h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=response_sent elapsed_ms=%d transport=json",
			idKey, time.Since(started).Milliseconds())
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

// handlePostStreaming serves a request as text/event-stream and emits
// periodic heartbeats while waiting for the wrapped child's response.
// When the request body's params._meta.progressToken is present, each
// heartbeat is a JSON-RPC notifications/progress referencing that token
// (the spec's resetTimeoutOnProgress hook). When absent, heartbeats are
// SSE comment lines that only keep the TCP connection warm.
func (h *httpHandler) handlePostStreaming(
	w http.ResponseWriter,
	r *http.Request,
	idKey string,
	id json.RawMessage,
	body []byte,
	interval time.Duration,
	started time.Time,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=error elapsed_ms=%d err=%q",
			idKey, time.Since(started).Milliseconds(), "streaming unsupported")
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	progressToken := extractProgressToken(body)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	type sendResult struct {
		resp json.RawMessage
		err  error
	}
	results := make(chan sendResult, 1)
	go func() {
		resp, err := h.t.SendRequest(r.Context(), idKey, body)
		results <- sendResult{resp: resp, err: err}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var seq int64

	for {
		select {
		case <-r.Context().Done():
			h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=ctx_canceled elapsed_ms=%d transport=sse heartbeats=%d",
				idKey, time.Since(started).Milliseconds(), seq)
			return
		case res := <-results:
			if res.err != nil {
				if errors.Is(res.err, context.Canceled) || errors.Is(res.err, context.DeadlineExceeded) {
					h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=ctx_canceled elapsed_ms=%d transport=sse heartbeats=%d",
						idKey, time.Since(started).Milliseconds(), seq)
					return
				}
				code := codeInvalidRequest
				outcome := "error"
				if errors.Is(res.err, ErrQueueFull) {
					code = codeBridgeQueueFull
					outcome = "queue_full"
				}
				h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=%s elapsed_ms=%d transport=sse heartbeats=%d err=%q",
					idKey, outcome, time.Since(started).Milliseconds(), seq, res.err.Error())
				errMsg, _ := json.Marshal(jsonRPCError{
					JSONRPC: "2.0",
					ID:      id,
					Error: jsonRPCErrorObj{
						Code:    code,
						Message: "clown-stdio-bridge: " + res.err.Error(),
					},
				})
				fmt.Fprintf(w, "data: %s\n\n", errMsg)
				flusher.Flush()
				return
			}
			h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=response_sent elapsed_ms=%d transport=sse heartbeats=%d",
				idKey, time.Since(started).Milliseconds(), seq)
			fmt.Fprintf(w, "data: %s\n\n", res.resp)
			flusher.Flush()
			return
		case <-ticker.C:
			seq++
			heartbeatKind := "comment"
			if len(progressToken) > 0 {
				heartbeatKind = "progress"
				notif := fmt.Sprintf(
					`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%s,"progress":%d,"message":"clown-stdio-bridge: still waiting"}}`,
					progressToken, seq)
				fmt.Fprintf(w, "data: %s\n\n", notif)
			} else {
				fmt.Fprintf(w, ": heartbeat %d\n\n", seq)
			}
			flusher.Flush()
			h.logger.Printf("clown-stdio-bridge: heartbeat id=%s seq=%d kind=%s elapsed_ms=%d",
				idKey, seq, heartbeatKind, time.Since(started).Milliseconds())
		}
	}
}

// extractProgressToken returns the JSON-encoded progressToken from a
// JSON-RPC request body's params._meta, or nil if not present. Returns
// the raw token bytes so they can be inlined verbatim — preserving
// whether the client sent a string ("abc123") or an integer (42).
func extractProgressToken(body []byte) json.RawMessage {
	var probe struct {
		Params struct {
			Meta struct {
				ProgressToken json.RawMessage `json:"progressToken"`
			} `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	tok := probe.Params.Meta.ProgressToken
	if len(tok) == 0 || string(tok) == "null" {
		return nil
	}
	return tok
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

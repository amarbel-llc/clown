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
	"strings"
	"time"
)

// heartbeatEnvVar selects the cadence at which handlePost emits keep-alive
// activity on streaming responses. Unset uses heartbeatDefault. "0" or
// "off" disables heartbeats AND falls back to plain application/json
// responses (legacy behavior). Any other value is parsed by
// time.ParseDuration.
const heartbeatEnvVar = "CLOWN_BRIDGE_HEARTBEAT_INTERVAL"

// heartbeatModeEnvVar selects a named heartbeat mode that overrides the
// interval-derived policy. Recognized overrides:
//   - "forward-only" (alias "child"): keep SSE streaming on so child
//     notifications/progress and the final response are delivered, but
//     suppress the bridge's own timer so heartbeats are activity-driven by the
//     child alone.
//   - "forward-only+fallback" (alias "child+fallback"): like forward-only, but
//     arm an ACTIVITY-GATED fallback timer — the bridge emits a heartbeat only
//     after the child has been silent for heartbeatEnvVar's interval, re-arming
//     on every child message. A bridge-side keep-alive ceiling that still lets
//     a genuinely hung child time out (clown#109).
//
// Unset or any unrecognized value falls back to the heartbeatEnvVar cadence.
const heartbeatModeEnvVar = "CLOWN_BRIDGE_HEARTBEAT"

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

// heartbeatMode resolves the per-POST streaming/timer policy from the two
// heartbeat env vars. It reports whether the response should stream
// (text/event-stream), the timer interval, and whether that interval is
// activity-gated (fallback). heartbeatModeEnvVar takes precedence.
//
//   - "forward-only"/"child": stream, no bridge timer (interval 0) — keep-alive
//     is the child's own notifications/progress alone.
//   - "forward-only+fallback"/"child+fallback": stream, and arm an
//     activity-gated fallback timer (fallback=true) whose threshold reuses
//     heartbeatInterval() (heartbeatDefault when that is 0/off).
//   - otherwise: the heartbeatEnvVar cadence as a fixed-interval timer
//     (fallback=false; streaming when the cadence is > 0).
func heartbeatMode() (streaming bool, interval time.Duration, fallback bool) {
	if v, set := os.LookupEnv(heartbeatModeEnvVar); set {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "forward-only", "child":
			return true, 0, false
		case "forward-only+fallback", "child+fallback":
			iv := heartbeatInterval()
			if iv <= 0 {
				iv = heartbeatDefault
			}
			return true, iv, true
		}
	}
	iv := heartbeatInterval()
	return iv > 0, iv, false
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
	// stats emits per-request duration + outcome metrics to statsd
	// (stats-me). Nil disables emission (see statsd.go).
	stats *statsdClient
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
		label := metricLabel(probe.Method, body)
		h.logger.Printf(
			"clown-stdio-bridge: post start id=%s method=%q has_progressToken=%t body_size=%d",
			idKey, probe.Method, hasToken, len(body))
		if streaming, interval, fallback := heartbeatMode(); streaming {
			h.handlePostStreaming(w, r, idKey, probe.ID, body, interval, fallback, started, label)
			return
		}
		// Synchronous JSON response (streaming disabled).
		resp, err := h.t.SendRequest(r.Context(), idKey, body)
		if err != nil {
			if errors.Is(err, ErrQueueFull) {
				h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=queue_full elapsed_ms=%d",
					idKey, time.Since(started).Milliseconds())
				h.stats.emitOutcome(label, started, "failure")
				writeJSONRPCError(w, http.StatusServiceUnavailable, probe.ID,
					codeBridgeQueueFull,
					"clown-stdio-bridge: inbound queue saturated")
				return
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Client disconnected; nothing useful to send.
				h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=ctx_canceled elapsed_ms=%d",
					idKey, time.Since(started).Milliseconds())
				h.stats.emitOutcome(label, started, "abandoned")
				return
			}
			h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=error elapsed_ms=%d err=%q",
				idKey, time.Since(started).Milliseconds(), err.Error())
			h.stats.emitOutcome(label, started, "failure")
			writeJSONRPCError(w, http.StatusInternalServerError, probe.ID,
				codeInvalidRequest,
				"clown-stdio-bridge: "+err.Error())
			return
		}
		h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=response_sent elapsed_ms=%d transport=json",
			idKey, time.Since(started).Milliseconds())
		h.stats.emitOutcome(label, started, responseOutcome(resp))
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
// SSE comment lines that only keep the TCP connection warm. When interval
// is 0 (forward-only mode) no heartbeats are emitted at all: the stream
// carries only the child's own notifications and the final response.
func (h *httpHandler) handlePostStreaming(
	w http.ResponseWriter,
	r *http.Request,
	idKey string,
	id json.RawMessage,
	body []byte,
	interval time.Duration,
	fallback bool,
	started time.Time,
	label string,
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
		defer func() {
			if rec := recover(); rec != nil {
				h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=panic elapsed_ms=%d panic=%v",
					idKey, time.Since(started).Milliseconds(), rec)
				results <- sendResult{err: fmt.Errorf("clown-stdio-bridge: SendRequest panicked: %v", rec)}
			}
		}()
		resp, err := h.t.SendRequest(r.Context(), idKey, body)
		results <- sendResult{resp: resp, err: err}
	}()

	// Timer policy:
	//   - forward-only (interval == 0): no timer at all — nil channels make the
	//     timer cases below unreachable (time.NewTicker/NewTimer also panic on a
	//     non-positive duration, so they must be skipped).
	//   - fixed cadence (interval > 0, !fallback): a periodic ticker fires every
	//     interval regardless of child activity (the original behavior).
	//   - activity-gated fallback (interval > 0, fallback): a resettable timer
	//     fires only after the child has been silent for interval; every child
	//     message — observed via the translator broadcast — re-arms it, so a
	//     slow-but-progressing call stays alive while a genuinely hung child
	//     eventually stops re-arming and times out (clown#109).
	var (
		tickC    <-chan time.Time       // fixed-cadence ticker
		silenceC <-chan time.Time       // activity-gated fallback timer's channel
		silence  *time.Timer            // backing timer for silenceC
		activity <-chan json.RawMessage // child-output signal (fallback only)
	)
	switch {
	case fallback && interval > 0:
		silence = time.NewTimer(interval)
		defer silence.Stop()
		silenceC = silence.C
		var cancel func()
		activity, cancel = h.t.Subscribe()
		defer cancel()
	case interval > 0:
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tickC = ticker.C
	}
	var seq int64

	emitHeartbeat := func() {
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

	for {
		select {
		case <-r.Context().Done():
			h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=ctx_canceled elapsed_ms=%d transport=sse heartbeats=%d",
				idKey, time.Since(started).Milliseconds(), seq)
			h.stats.emitOutcome(label, started, "abandoned")
			return
		case res := <-results:
			if res.err != nil {
				if errors.Is(res.err, context.Canceled) || errors.Is(res.err, context.DeadlineExceeded) {
					h.logger.Printf("clown-stdio-bridge: post end id=%s outcome=ctx_canceled elapsed_ms=%d transport=sse heartbeats=%d",
						idKey, time.Since(started).Milliseconds(), seq)
					h.stats.emitOutcome(label, started, "abandoned")
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
				h.stats.emitOutcome(label, started, "failure")
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
			h.stats.emitOutcome(label, started, responseOutcome(res.resp))
			fmt.Fprintf(w, "data: %s\n\n", res.resp)
			flusher.Flush()
			return
		case <-activity:
			// Child produced output: it is progressing, so re-arm the silence
			// window. We only OBSERVE here (the GET stream forwards child
			// notifications to its own subscribers); we do not re-emit. activity
			// is nil outside fallback mode, so this case never fires there.
			if !silence.Stop() {
				select {
				case <-silence.C:
				default:
				}
			}
			silence.Reset(interval)
		case <-tickC:
			emitHeartbeat()
		case <-silenceC:
			// Fallback: the child has been silent for interval — emit one
			// heartbeat, then re-arm to fire again after continued silence.
			emitHeartbeat()
			silence.Reset(interval)
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

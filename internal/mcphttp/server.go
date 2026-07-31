package mcphttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Heartbeat is the resolved per-POST streaming/timer policy the spine applies
// while awaiting an upstream response. The caller resolves it (from env, flags,
// or defaults) and passes it in via Config, so the spine carries no policy
// source of its own and two consumers in one process can differ.
//
// The three shapes the timer logic supports:
//   - Streaming false: no SSE; the POST is answered as plain application/json.
//   - Streaming true, Interval 0: SSE with no spine timer — keep-alive is the
//     upstream's own notifications/progress alone (forward-only).
//   - Streaming true, Interval > 0, Fallback false: SSE with a fixed-cadence
//     ticker firing every Interval regardless of upstream activity.
//   - Streaming true, Interval > 0, Fallback true: SSE with an activity-gated
//     timer that fires only after the upstream has been silent for Interval,
//     re-arming on every upstream message.
type Heartbeat struct {
	// Streaming reports whether the response streams as text/event-stream. When
	// false the POST is answered synchronously as application/json.
	Streaming bool
	// Interval is the heartbeat/silence timer cadence. Zero means no spine timer
	// (forward-only) even when Streaming is true.
	Interval time.Duration
	// Fallback makes Interval an activity-gated silence threshold (re-armed by
	// upstream output) rather than a fixed cadence. Ignored when Interval is 0.
	Fallback bool
}

// logger is a tiny abstraction so callers can capture log lines without
// pulling in slog. Production uses log.Printf via a *log.Logger.
type logger interface {
	Printf(format string, args ...any)
}

// RequestHandler is the pluggable upstream the Server dispatches to: given a
// JSON-RPC request it returns the matching JSON-RPC response body. The stdio
// bridge backs this with its single-child translator; the mcp-collapse
// aggregator will back it with an N-upstream dispatcher.
//
// The method set mirrors the bridge translator's exactly, so the translator
// satisfies this interface with no changes.
type RequestHandler interface {
	// SendRequest sends a JSON-RPC request and blocks until the matching
	// response body arrives or ctx is canceled. idKey is the JSON-encoded id.
	// Returns ErrQueueFull when the upstream's inbound queue is saturated.
	SendRequest(ctx context.Context, idKey string, body []byte) (json.RawMessage, error)
	// SendNotification is fire-and-forget: notifications and client responses
	// to server-initiated requests. Returns ErrQueueFull on saturation.
	SendNotification(body []byte) error
	// Subscribe returns a channel of server-initiated messages (for the GET
	// SSE stream) and a cancel func that unregisters the subscriber.
	Subscribe() (<-chan json.RawMessage, func())
}

// ErrQueueFull is the backpressure sentinel a RequestHandler returns when its
// bounded inbound queue cannot accept another message. The Server maps it to
// the codeQueueFull JSON-RPC error. The bridge's translator returns this
// exact sentinel (see clown-stdio-bridge, which aliases it) so errors.Is
// matches across the interface boundary.
var ErrQueueFull = errors.New("mcphttp: inbound queue full")

// Stats records per-request terminal outcomes for metrics emission. The
// concrete implementation (e.g. the bridge's statsd client) stays caller-side
// so the spine carries no metrics backend. A nil Stats disables emission.
type Stats interface {
	// Label derives the per-request metric label from the JSON-RPC method and
	// full request body (e.g. the tool name for tools/call).
	Label(method string, body []byte) string
	// EmitOutcome records one request's terminal outcome (and, for completed
	// responses, its duration). outcome is the backend-independent string the
	// spine classifies (e.g. "success"/"failure"/"abandoned").
	EmitOutcome(label string, started time.Time, outcome string)
}

// ResponseFilter is an optional post-response hook invoked on a successful
// request response before it is written to the client, keyed by the request's
// JSON-RPC method. The bridge supplies it to strip excluded tools from
// tools/list responses (--cheap-context); returning body unchanged is a no-op.
type ResponseFilter func(method string, body json.RawMessage) json.RawMessage

// Config configures a Server. Handler is required; the rest are optional.
type Config struct {
	// Handler is the upstream transport (required).
	Handler RequestHandler
	// Logger receives the server's structured log lines. Required in practice;
	// the caller supplies its own so the log prefix matches its binary.
	Logger logger
	// LogPrefix is prepended to every log line and to error messages surfaced
	// to the client (e.g. "clown-stdio-bridge"). The Server appends ": " after
	// it. Empty yields "mcphttp".
	LogPrefix string
	// Heartbeat is the resolved streaming/timer policy applied to request POSTs.
	// The zero value (Streaming false) answers POSTs synchronously as
	// application/json — the caller supplies its own resolved policy.
	Heartbeat Heartbeat
	// Stats emits per-request outcome metrics. Nil disables emission.
	Stats Stats
	// Filter, when non-nil, post-processes each successful response body keyed
	// by request method. Nil passes responses through unchanged.
	Filter ResponseFilter
}

// Server is the reusable MCP-over-HTTP spine: it implements the
// streamable-HTTP request/response shape (POST → JSON or SSE, GET → SSE),
// origin checks, heartbeats, and JSON-RPC error framing, dispatching every
// request to a pluggable RequestHandler.
type Server struct {
	h         RequestHandler
	logger    logger
	logPrefix string
	heartbeat Heartbeat
	stats     Stats
	filter    ResponseFilter
}

// NewServer builds a Server from cfg. Handler must be non-nil.
//
// A Heartbeat{Fallback: true, Interval: 0} is a misconfiguration: the streaming
// timer switch matches neither the fallback branch (which needs Interval > 0)
// nor the fixed-cadence branch, so the activity-gated keep-alive the caller
// asked for would silently never fire (plain forward-only). NewServer doesn't
// return an error, so rather than change its signature it coerces Fallback off
// and logs a warning via cfg.Logger — the policy becomes honest forward-only
// and the misconfiguration is no longer invisible.
func NewServer(cfg Config) *Server {
	prefix := cfg.LogPrefix
	if prefix == "" {
		prefix = "mcphttp"
	}
	hb := cfg.Heartbeat
	if hb.Fallback && hb.Interval <= 0 {
		if cfg.Logger != nil {
			cfg.Logger.Printf(
				"%s: Heartbeat.Fallback set with non-positive Interval (%v); "+
					"activity-gated keep-alive cannot arm — coercing to forward-only",
				prefix, hb.Interval,
			)
		}
		hb.Fallback = false
	}
	return &Server{
		h:         cfg.Handler,
		logger:    cfg.Logger,
		logPrefix: prefix,
		heartbeat: hb,
		stats:     cfg.Stats,
		filter:    cfg.Filter,
	}
}

// logf logs a line under the server's prefix, matching the bridge's original
// "<prefix>: <msg>" shape.
func (s *Server) logf(format string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Printf(s.logPrefix+": "+format, args...)
}

// label derives the metric label via Stats, or "" when stats are disabled.
func (s *Server) label(method string, body []byte) string {
	if s.stats == nil {
		return ""
	}
	return s.stats.Label(method, body)
}

// emitOutcome records a terminal outcome via Stats when enabled.
func (s *Server) emitOutcome(label string, started time.Time, outcome string) {
	if s.stats != nil {
		s.stats.EmitOutcome(label, started, outcome)
	}
}

// responseOutcome classifies a delivered JSON-RPC response as "failure" when
// it carries a non-null error member, else "success". Backend-independent —
// the spine already parses every response, so this stays in the spine rather
// than routing through the Stats backend.
func responseOutcome(resp json.RawMessage) string {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(resp, &probe) != nil {
		return "success"
	}
	if len(probe.Error) > 0 && string(probe.Error) != "null" {
		return "failure"
	}
	return "success"
}

// filterResponse applies the configured response filter, if any.
func (s *Server) filterResponse(method string, body json.RawMessage) json.RawMessage {
	if s.filter == nil {
		return body
	}
	return s.filter(method, body)
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
	codeQueueFull      = -32000
	codeParseError     = -32700
	codeInvalidRequest = -32600
)

// HandleMCP routes the /mcp endpoint: POST (request/notification), GET (SSE
// stream of server-initiated messages), DELETE (405 — no session
// termination), else 405. Origin is restricted to loopback.
func (s *Server) HandleMCP(w http.ResponseWriter, r *http.Request) {
	if !ValidateOrigin(r) {
		http.Error(w, "origin not permitted", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.handlePost(w, r)
	case http.MethodGet:
		s.handleGet(w, r)
	case http.MethodDelete:
		http.Error(w, "session termination not supported", http.StatusMethodNotAllowed)
	default:
		w.Header().Set("Allow", "POST, GET, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
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
		label := s.label(probe.Method, body)
		s.logf(
			"post start id=%s method=%q has_progressToken=%t body_size=%d",
			idKey, probe.Method, hasToken, len(body),
		)
		if s.heartbeat.Streaming {
			s.handlePostStreaming(w, r, idKey, probe.ID, probe.Method, body, s.heartbeat.Interval, s.heartbeat.Fallback, started, label)
			return
		}
		// Synchronous JSON response (streaming disabled).
		resp, err := s.h.SendRequest(r.Context(), idKey, body)
		if err != nil {
			if errors.Is(err, ErrQueueFull) {
				s.logf("post end id=%s outcome=queue_full elapsed_ms=%d",
					idKey, time.Since(started).Milliseconds())
				s.emitOutcome(label, started, "failure")
				writeJSONRPCError(w, http.StatusServiceUnavailable, probe.ID,
					codeQueueFull,
					s.logPrefix+": inbound queue saturated")
				return
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Client disconnected; nothing useful to send.
				s.logf("post end id=%s outcome=ctx_canceled elapsed_ms=%d",
					idKey, time.Since(started).Milliseconds())
				s.emitOutcome(label, started, "abandoned")
				return
			}
			s.logf("post end id=%s outcome=error elapsed_ms=%d err=%q",
				idKey, time.Since(started).Milliseconds(), err.Error())
			s.emitOutcome(label, started, "failure")
			writeJSONRPCError(w, http.StatusInternalServerError, probe.ID,
				codeInvalidRequest,
				s.logPrefix+": "+err.Error())
			return
		}
		s.logf("post end id=%s outcome=response_sent elapsed_ms=%d transport=json",
			idKey, time.Since(started).Milliseconds())
		s.emitOutcome(label, started, responseOutcome(resp))
		resp = s.filterResponse(probe.Method, resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	case hasMethod && !hasID:
		// Notification — fire-and-forget.
		if err := s.h.SendNotification(body); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	case !hasMethod && hasID:
		// Response from client (e.g., to a server-initiated request).
		// Forward fire-and-forget; the upstream will route by id.
		if err := s.h.SendNotification(body); err != nil {
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
// periodic heartbeats while waiting for the upstream's response.
// When the request body's params._meta.progressToken is present, each
// heartbeat is a JSON-RPC notifications/progress referencing that token
// (the spec's resetTimeoutOnProgress hook). When absent, heartbeats are
// SSE comment lines that only keep the TCP connection warm. When interval
// is 0 (forward-only mode) no heartbeats are emitted at all: the stream
// carries only the upstream's own notifications and the final response.
func (s *Server) handlePostStreaming(
	w http.ResponseWriter,
	r *http.Request,
	idKey string,
	id json.RawMessage,
	method string,
	body []byte,
	interval time.Duration,
	fallback bool,
	started time.Time,
	label string,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.logf("post end id=%s outcome=error elapsed_ms=%d err=%q",
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
				s.logf("post end id=%s outcome=panic elapsed_ms=%d panic=%v",
					idKey, time.Since(started).Milliseconds(), rec)
				results <- sendResult{err: fmt.Errorf("%s: SendRequest panicked: %v", s.logPrefix, rec)}
			}
		}()
		resp, err := s.h.SendRequest(r.Context(), idKey, body)
		results <- sendResult{resp: resp, err: err}
	}()

	// Timer policy:
	//   - forward-only (interval == 0): no timer at all — nil channels make the
	//     timer cases below unreachable (time.NewTicker/NewTimer also panic on a
	//     non-positive duration, so they must be skipped).
	//   - fixed cadence (interval > 0, !fallback): a periodic ticker fires every
	//     interval regardless of upstream activity (the original behavior).
	//   - activity-gated fallback (interval > 0, fallback): a resettable timer
	//     fires only after the upstream has been silent for interval; every
	//     upstream message — observed via the Subscribe broadcast — re-arms it,
	//     so a slow-but-progressing call stays alive while a genuinely hung
	//     upstream eventually stops re-arming and times out (clown#109).
	var (
		tickC    <-chan time.Time       // fixed-cadence ticker
		silenceC <-chan time.Time       // activity-gated fallback timer's channel
		silence  *time.Timer            // backing timer for silenceC
		activity <-chan json.RawMessage // upstream-output signal (fallback only)
	)
	switch {
	case fallback && interval > 0:
		silence = time.NewTimer(interval)
		defer silence.Stop()
		silenceC = silence.C
		var cancel func()
		activity, cancel = s.h.Subscribe()
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
				`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%s,"progress":%d,"message":"%s: still waiting"}}`,
				progressToken, seq, s.logPrefix,
			)
			fmt.Fprintf(w, "data: %s\n\n", notif)
		} else {
			fmt.Fprintf(w, ": heartbeat %d\n\n", seq)
		}
		flusher.Flush()
		s.logf("heartbeat id=%s seq=%d kind=%s elapsed_ms=%d",
			idKey, seq, heartbeatKind, time.Since(started).Milliseconds())
	}

	for {
		select {
		case <-r.Context().Done():
			s.logf("post end id=%s outcome=ctx_canceled elapsed_ms=%d transport=sse heartbeats=%d",
				idKey, time.Since(started).Milliseconds(), seq)
			s.emitOutcome(label, started, "abandoned")
			return
		case res := <-results:
			if res.err != nil {
				if errors.Is(res.err, context.Canceled) || errors.Is(res.err, context.DeadlineExceeded) {
					s.logf("post end id=%s outcome=ctx_canceled elapsed_ms=%d transport=sse heartbeats=%d",
						idKey, time.Since(started).Milliseconds(), seq)
					s.emitOutcome(label, started, "abandoned")
					return
				}
				code := codeInvalidRequest
				outcome := "error"
				if errors.Is(res.err, ErrQueueFull) {
					code = codeQueueFull
					outcome = "queue_full"
				}
				s.logf("post end id=%s outcome=%s elapsed_ms=%d transport=sse heartbeats=%d err=%q",
					idKey, outcome, time.Since(started).Milliseconds(), seq, res.err.Error())
				s.emitOutcome(label, started, "failure")
				errMsg, _ := json.Marshal(jsonRPCError{
					JSONRPC: "2.0",
					ID:      id,
					Error: jsonRPCErrorObj{
						Code:    code,
						Message: s.logPrefix + ": " + res.err.Error(),
					},
				})
				fmt.Fprintf(w, "data: %s\n\n", errMsg)
				flusher.Flush()
				return
			}
			s.logf("post end id=%s outcome=response_sent elapsed_ms=%d transport=sse heartbeats=%d",
				idKey, time.Since(started).Milliseconds(), seq)
			s.emitOutcome(label, started, responseOutcome(res.resp))
			resp := s.filterResponse(method, res.resp)
			fmt.Fprintf(w, "data: %s\n\n", resp)
			flusher.Flush()
			return
		case <-activity:
			// Upstream produced output: it is progressing, so re-arm the silence
			// window. We only OBSERVE here (the GET stream forwards upstream
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
			// Fallback: the upstream has been silent for interval — emit one
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

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
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

	sub, cancel := s.h.Subscribe()
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

// ValidateOrigin enforces the loopback-only posture mandated by the
// streamable-HTTP spec's security warning. Empty Origin is permitted
// (curl, programmatic clients without a browser context). Exported so callers
// can gate their own sibling control endpoints (e.g. the bridge's
// /clown/exclude-tools) with the identical policy HandleMCP applies to /mcp.
func ValidateOrigin(r *http.Request) bool {
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

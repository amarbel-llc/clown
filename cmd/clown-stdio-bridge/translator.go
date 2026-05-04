package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// traceEnvVar gates the full passthrough log of every JSON-RPC line in
// either direction between bridge and wrapped child. Set to a truthy
// value ("1", "true", "yes") to enable. Heavy; intended for upstream
// bug reports, not normal operation.
const traceEnvVar = "CLOWN_BRIDGE_TRACE"

func tracingEnabled() bool {
	switch os.Getenv(traceEnvVar) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// queueDepth is the bounded queue size for stdin writes and per-subscriber
// SSE broadcasts. Per FDR 0002 §"Resource limits". Tunable; future
// implementing RFC may surface this as a flag.
const queueDepth = 256

// writerLivenessEnvVar selects the cadence at which runWriter emits a
// liveness log line proving the goroutine is still scheduled. Empty/unset
// uses writerLivenessDefault. "0" or "off" disables. Other values are
// parsed by time.ParseDuration. The signal exists so a deadlocked
// stdin.Write (child not draining stdin) is observable in real time
// rather than only via a downstream HTTP timeout hours later.
const writerLivenessEnvVar = "CLOWN_BRIDGE_WRITER_LIVENESS_INTERVAL"

const writerLivenessDefault = 30 * time.Second

func writerLivenessInterval() time.Duration {
	v, set := os.LookupEnv(writerLivenessEnvVar)
	if !set {
		return writerLivenessDefault
	}
	switch v {
	case "0", "off", "":
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return writerLivenessDefault
	}
	return d
}

// translator routes JSON-RPC messages between the wrapped stdio MCP
// server's stdin/stdout and the bridge's HTTP layer.
//
// Inbound (HTTP → child): callers invoke SendRequest (blocking, returns
// the matching response) or SendNotification (fire-and-forget). Both
// land in writeQueue; a single goroutine drains writeQueue to stdin
// in arrival order.
//
// Outbound (child → HTTP): a single goroutine reads child stdout
// line-by-line, parses JSON-RPC, and demuxes:
//   - response (has id, no method) → matching pendingRequest channel
//   - request or notification (has method) → broadcast to every active
//     SSE subscriber
//
// Bounded queues bound memory. Inbound overflow returns an error to the
// caller (which surfaces as an MCP-level error to the HTTP client).
// Outbound overflow drops the oldest message in the offending
// subscriber's channel.
type translator struct {
	stdin  io.Writer
	stdout io.Reader

	writeQueue chan []byte

	pendingMu sync.Mutex
	// pending is keyed by JSON-RPC id. An entry's respCh is non-nil while
	// a SendRequest is actively waiting; it transitions to nil ("abandoned"
	// state) when SendRequest exits without delivery (typically ctx
	// cancel). The entry survives in either state so:
	//   - new SendRequests for the same id reject with duplicate-id rather
	//     than receiving the abandoned call's late response (the #50
	//     cross-contamination bug)
	//   - demux can log elapsed-time on the late-response path and
	//     distinguish "abandoned" from "true orphan" (id never seen)
	pending map[string]pendingEntry

	subsMu      sync.Mutex
	subscribers map[*subscriber]struct{}

	logger logger
	trace  bool

	droppedOutbound atomic.Int64
}

// pendingEntry tracks one in-flight or abandoned request. respCh is nil
// for abandoned entries.
type pendingEntry struct {
	respCh chan json.RawMessage
	seenAt time.Time
}

type subscriber struct {
	ch chan json.RawMessage
}

// logger is a tiny abstraction so tests can capture log lines without
// pulling in slog. Production uses log.Printf via a *log.Logger.
type logger interface {
	Printf(format string, args ...any)
}

// newTranslator creates a translator over the wrapped child's
// stdin/stdout. Run drives both directions.
func newTranslator(stdin io.Writer, stdout io.Reader, log logger) *translator {
	return &translator{
		stdin:       stdin,
		stdout:      stdout,
		writeQueue:  make(chan []byte, queueDepth),
		pending:     make(map[string]pendingEntry),
		subscribers: make(map[*subscriber]struct{}),
		logger:      log,
		trace:       tracingEnabled(),
	}
}

// Run starts the writer and reader goroutines and blocks until ctx is
// canceled or the reader hits EOF / a fatal error. Errors from the
// child's stdout are returned; ctx cancellation returns nil.
func (t *translator) Run(ctx context.Context) error {
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		t.runWriter(ctx)
	}()

	readerErr := make(chan error, 1)
	go func() { readerErr <- t.runReader() }()

	select {
	case <-ctx.Done():
		<-writerDone
		// Don't wait for reader: stdout EOF arrives only when the
		// child exits, which is the caller's responsibility.
		return nil
	case err := <-readerErr:
		<-writerDone
		return err
	}
}

func (t *translator) runWriter(ctx context.Context) {
	livenessInterval := writerLivenessInterval()
	var livenessC <-chan time.Time
	if livenessInterval > 0 {
		ticker := time.NewTicker(livenessInterval)
		defer ticker.Stop()
		livenessC = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-livenessC:
			t.logger.Printf("clown-stdio-bridge: writer alive queue_depth=%d pending=%d",
				len(t.writeQueue), t.pendingActiveCount())
		case msg := <-t.writeQueue:
			if t.trace {
				t.logger.Printf("clown-stdio-bridge: trace stdin %s", tracePreview(msg))
			}
			if _, err := t.stdin.Write(msg); err != nil {
				t.logExitOrphans("write to child stdin failed", err)
				return
			}
			if _, err := t.stdin.Write([]byte{'\n'}); err != nil {
				t.logExitOrphans("write newline to child stdin failed", err)
				return
			}
		}
	}
}

// pendingActiveCount returns the number of pending entries with a live
// respCh (i.e. an HTTP handler is currently blocked waiting on the
// child's response). Excludes abandoned entries.
func (t *translator) pendingActiveCount() int {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	n := 0
	for _, entry := range t.pending {
		if entry.respCh != nil {
			n++
		}
	}
	return n
}

// logExitOrphans is the runWriter exit logger. It surfaces both the
// underlying error and the count of in-flight requests that will now
// hang on respCh because the writer is gone — the failure mode that
// makes long-running calls invisible after the fact.
func (t *translator) logExitOrphans(reason string, err error) {
	t.logger.Printf("clown-stdio-bridge: %s: %v; writer exiting; pending_orphaned=%d",
		reason, err, t.pendingActiveCount())
}

func (t *translator) runReader() error {
	scanner := bufio.NewScanner(t.stdout)
	// Allow long lines; MCP messages can carry sizable params/results.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if t.trace {
			t.logger.Printf("clown-stdio-bridge: trace stdout %s", tracePreview(line))
		}
		t.demux(line)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read child stdout: %w", err)
	}
	return nil
}

// tracePreview formats a JSON-RPC line for trace logging: keeps it on one
// line, caps length, and surfaces id/method when available.
func tracePreview(line []byte) string {
	const maxBody = 256
	body := line
	if len(body) > maxBody {
		body = append(append([]byte{}, body[:maxBody]...), []byte("…")...)
	}
	return fmt.Sprintf("length=%d body=%s", len(line), body)
}

// demux routes a single line of child stdout to either the matching
// pending request channel (if it's a response) or to all SSE
// subscribers (if it's a request or notification).
func (t *translator) demux(line []byte) {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		t.logger.Printf("clown-stdio-bridge: child wrote non-JSON line: %v (line=%q)", err, line)
		return
	}

	hasID := len(probe.ID) > 0 && string(probe.ID) != "null"
	hasMethod := probe.Method != ""

	switch {
	case hasID && !hasMethod:
		// Response — route to pending request, or log+drop if the
		// addressed call has already been abandoned (#50).
		idKey := string(probe.ID)
		t.pendingMu.Lock()
		entry, ok := t.pending[idKey]
		if ok {
			delete(t.pending, idKey)
		}
		t.pendingMu.Unlock()
		switch {
		case !ok:
			t.logger.Printf("clown-stdio-bridge: response for unknown id %s elapsed=unknown", idKey)
			return
		case entry.respCh == nil:
			// Late response from a SendRequest that already returned
			// (ctx cancel). Drop it — delivering would cross-contaminate
			// any future SendRequest that reuses this id.
			t.logger.Printf("clown-stdio-bridge: response for abandoned id %s elapsed=%dms",
				idKey, time.Since(entry.seenAt).Milliseconds())
			return
		}
		// Buffered channel of size 1 — never blocks.
		entry.respCh <- line
	case hasMethod:
		// Request or notification — broadcast.
		t.broadcast(line)
	default:
		t.logger.Printf("clown-stdio-bridge: malformed JSON-RPC line (no id, no method): %q", line)
	}
}

func (t *translator) broadcast(msg json.RawMessage) {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()
	for sub := range t.subscribers {
		select {
		case sub.ch <- msg:
		default:
			// Subscriber's channel is full. Drop the oldest and
			// requeue. Counter exposed via DroppedOutbound for
			// diagnosis.
			select {
			case <-sub.ch:
				t.droppedOutbound.Add(1)
			default:
			}
			select {
			case sub.ch <- msg:
			default:
				t.droppedOutbound.Add(1)
			}
		}
	}
}

// Subscribe registers a new SSE subscriber. The returned channel
// receives broadcast messages until cancel is called.
func (t *translator) Subscribe() (<-chan json.RawMessage, func()) {
	sub := &subscriber{ch: make(chan json.RawMessage, queueDepth)}
	t.subsMu.Lock()
	t.subscribers[sub] = struct{}{}
	t.subsMu.Unlock()
	cancel := func() {
		t.subsMu.Lock()
		delete(t.subscribers, sub)
		t.subsMu.Unlock()
	}
	return sub.ch, cancel
}

// SendRequest writes a JSON-RPC request line to the child's stdin and
// blocks until the matching response arrives or ctx is canceled. idKey
// MUST be the JSON-encoded form of the request's id field. The msg
// argument is the full JSON-RPC request body (will be written verbatim
// followed by a newline).
//
// Returns ErrQueueFull if the inbound queue is saturated.
func (t *translator) SendRequest(ctx context.Context, idKey string, msg []byte) (json.RawMessage, error) {
	respCh := make(chan json.RawMessage, 1)
	t.pendingMu.Lock()
	if existing, exists := t.pending[idKey]; exists {
		t.pendingMu.Unlock()
		if existing.respCh == nil {
			return nil, fmt.Errorf(
				"abandoned JSON-RPC id %s still in flight (prior call canceled, response not yet delivered)",
				idKey)
		}
		return nil, fmt.Errorf("duplicate JSON-RPC id %s in flight", idKey)
	}
	t.pending[idKey] = pendingEntry{respCh: respCh, seenAt: time.Now()}
	t.pendingMu.Unlock()

	defer func() {
		t.pendingMu.Lock()
		// If demux already delivered, the entry is gone — done. If our
		// entry is still there, transition it to abandoned (respCh=nil)
		// so any late response from the wrapped child is logged and
		// dropped rather than cross-contaminating a future SendRequest
		// that reuses this id.
		if entry, ok := t.pending[idKey]; ok && entry.respCh == respCh {
			t.pending[idKey] = pendingEntry{respCh: nil, seenAt: entry.seenAt}
		}
		t.pendingMu.Unlock()
	}()

	select {
	case t.writeQueue <- msg:
	default:
		return nil, ErrQueueFull
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SendNotification writes a JSON-RPC notification or response to the
// child's stdin. Fire-and-forget; returns ErrQueueFull if saturated.
func (t *translator) SendNotification(msg []byte) error {
	select {
	case t.writeQueue <- msg:
		return nil
	default:
		return ErrQueueFull
	}
}

// DroppedOutbound returns the cumulative count of broadcast messages
// dropped due to a subscriber's channel being full.
func (t *translator) DroppedOutbound() int64 { return t.droppedOutbound.Load() }

// ErrQueueFull is returned when the bounded inbound queue cannot accept
// another message. The caller should surface this to the HTTP client
// as an MCP-level error.
var ErrQueueFull = errors.New("clown-stdio-bridge: inbound queue full")

// Package bridgeshim is the INSIDE half of the bridge (FDR 0048, contract v1): a
// client that opens one stream to the session's bridge socket and carries a
// service's bytes over it, a stdio client (an MCP server entry, a hook command),
// a listening shim (a loopback port or a unix path that forwards each connection
// to a service), and the `control` stream whose end means "the bridge is down".
//
// The shim holds no secret and decides nothing. It is a pipe with a hello: what
// may cross is the server's policy, outside (breaking the shim gains the session
// nothing it could not already send). One connection per stream, no multiplexer.
package bridgeshim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

// EnvSocket names the bridge socket for programs the shim starts.
const EnvSocket = "CLOWN_BRIDGE_SOCKET"

// DialTimeout bounds the connect and the wait for the answer.
var DialTimeout = bridgewire.HelloTimeout

// RefusedError is an ERR answer from the server.
type RefusedError struct {
	Service string
	Code    string
	Text    string
}

func (e *RefusedError) Error() string {
	// The text comes off the socket: printable ASCII only on the terminal
	// (round 1, A-L6), so a replaced socket cannot write escape sequences.
	text := bridgewire.Printable(e.Text)
	if text == "" {
		return fmt.Sprintf("the bridge refused service %q: %s", e.Service, e.Code)
	}
	return fmt.Sprintf("the bridge refused service %q: %s: %s", e.Service, e.Code, text)
}

// DownError is a bridge that cannot be reached at all (no socket, a refused
// connect, the end of the stream before an answer). Its message is the contract's
// fixed text.
type DownError struct{ Reason string }

func (e *DownError) Error() string { return bridgewire.DownMessage(e.Reason) }

// Dial opens a stream to service on the bridge socket: it connects, sends the
// hello and reads the answer exactly to its newline. On OK the returned
// connection carries the service's bytes. An unreachable bridge is a *DownError;
// an ERR answer is a *RefusedError (a version refusal carries the server's hint).
func Dial(sock, service string) (net.Conn, error) {
	hello, err := bridgewire.FormatHello(bridgewire.Major, service)
	if err != nil {
		return nil, err
	}
	if len(sock) > bridgewire.MaxSocketPath {
		// A local fault, not a dead frontend: a plain error, not the down text
		// (round 2, L2).
		return nil, fmt.Errorf("clown: the bridge socket path is %d bytes, over the unix limit of %d; the bridge cannot be reached", len(sock), bridgewire.MaxSocketPath)
	}
	c, err := net.DialTimeout("unix", sock, DialTimeout)
	if err != nil {
		return nil, &DownError{Reason: "cannot connect to " + sock + ": " + shortErr(err)}
	}
	_ = c.SetDeadline(time.Now().Add(DialTimeout))
	if _, err := c.Write(hello); err != nil {
		c.Close()
		return nil, &DownError{Reason: "writing the hello: " + shortErr(err)}
	}
	ok, code, text, err := bridgewire.ReadReply(c)
	if err != nil {
		c.Close()
		return nil, &DownError{Reason: "no answer to the hello: " + shortErr(err)}
	}
	if !ok {
		c.Close()
		return nil, &RefusedError{Service: service, Code: code, Text: text}
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func shortErr(err error) string {
	var ne *net.OpError
	if errors.As(err, &ne) && ne.Err != nil {
		return ne.Err.Error()
	}
	return err.Error()
}

// pipe copies both ways until each side ends, half-closing the other.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	half := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		closeWrite(dst)
	}
	go half(b, a)
	go half(a, b)
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = c.Close()
	}
}

// Stdio is the stdio client: in to the stream and the stream to out, until both
// directions end. It returns the dial error unchanged, so the caller can print
// its message (the down text, or the server's refusal).
func Stdio(sock, service string, in io.Reader, out io.Writer) error {
	c, err := Dial(sock, service)
	if err != nil {
		return err
	}
	defer c.Close()
	inDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(c, in)
		close(inDone) // before the half-close, so the server's close that follows is ordered after it
		closeWrite(c)
	}()
	_, _ = io.Copy(out, c)
	select {
	case <-inDone:
		return nil
	default:
		// The server ended the stream while the client had not finished. Said at
		// once, not at the client's next byte (round 1, M1), and as what it is:
		// this one stream ended. It is not claimed to be the bridge (round 2,
		// L2/L3: a service may end a stream on purpose); the control stream and
		// the next dial are what say the bridge is down. The copy goroutine ends
		// with the process.
		return &StreamEndedError{Service: service}
	}
}

// StreamEndedError is a service's stream ending while the client still had
// input to send.
type StreamEndedError struct{ Service string }

func (e *StreamEndedError) Error() string {
	return fmt.Sprintf("clown: the bridge service %q ended the stream before the client was done", e.Service)
}

// Endpoint is one ordinary endpoint the shim offers inside the unit and the
// service its connections go to.
type Endpoint struct {
	Network string // "tcp" or "unix"
	Addr    string // 127.0.0.1:3128, or a path such as $SSH_AUTH_SOCK
	Service string
}

// Shim listens on its endpoints and forwards each accepted connection to the
// bridge. While the bridge is marked down every new connection is answered with
// the down text (an HTTP 503 on a tcp endpoint, so a client that speaks HTTP
// shows it; a unix endpoint closes) and the text is written to Log.
type Shim struct {
	Sock string
	Log  io.Writer

	mu        sync.Mutex
	held      bool      // the control stream is open
	lastLog   time.Time // the last rate-limited refusal line
	down      string    // the reason, "" while up
	downMsg   string    // the whole text a refused client is told
	listeners []net.Listener
	wg        sync.WaitGroup
}

// MarkDown makes the shim answer every endpoint with the down text.
func (s *Shim) MarkDown(reason string) { s.markDown(reason, bridgewire.DownMessage(reason)) }

// MarkUnusable is MarkDown for a cause that is not "the frontend is gone": a
// version mismatch, say, whose own text says what to do (round 2, L2). The
// text is shown as given.
func (s *Shim) MarkUnusable(text string) { s.markDown(text, text) }

func (s *Shim) markDown(reason, msg string) {
	s.mu.Lock()
	first := s.down == ""
	s.down = reason
	s.downMsg = msg
	if first {
		s.lastLog = time.Now() // this line is the first of the rate-limited refusals
	}
	s.mu.Unlock()
	if first && s.Log != nil {
		fmt.Fprintln(s.Log, msg)
	}
}

func (s *Shim) downMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.downMsg
}

// SetControlHeld records whether the `control` stream is open. While it is, a
// failed data dial fails only that connection.
func (s *Shim) SetControlHeld(held bool) {
	s.mu.Lock()
	s.held = held
	s.mu.Unlock()
}

func (s *Shim) controlHeld() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

// logRefusal writes a refusal to Log at most once a second, so a client that
// retries in a loop does not flood the terminal.
func (s *Shim) logRefusal(text string) {
	if s.Log == nil {
		return
	}
	s.mu.Lock()
	now := time.Now()
	ok := now.Sub(s.lastLog) >= time.Second
	if ok {
		s.lastLog = now
	}
	s.mu.Unlock()
	if ok {
		fmt.Fprintln(s.Log, text)
	}
}

// ClearDown undoes a mark-down: the control stream opening is proof the bridge
// is up (round 2, L1). A shim is marked down by a dial that failed before
// control was held; control opening later must not leave it down.
func (s *Shim) ClearDown() {
	s.mu.Lock()
	s.down = ""
	s.mu.Unlock()
}

// Down returns the reason the bridge is marked down, "" while it is up.
func (s *Shim) Down() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.down
}

// Listen starts every endpoint. It returns when all listen (or at the first
// that cannot, closing those already started).
func (s *Shim) Listen(eps []Endpoint) error {
	for _, e := range eps {
		if !bridgewire.ValidService(e.Service) {
			s.Close()
			return fmt.Errorf("endpoint %s %s: %q is not a service label", e.Network, e.Addr, e.Service)
		}
		ln, err := net.Listen(e.Network, e.Addr)
		if err != nil {
			s.Close()
			return fmt.Errorf("endpoint %s %s: %w", e.Network, e.Addr, err)
		}
		s.mu.Lock()
		s.listeners = append(s.listeners, ln)
		s.mu.Unlock()
		s.wg.Add(1)
		go s.accept(ln, e)
	}
	return nil
}

func (s *Shim) accept(ln net.Listener, e Endpoint) {
	defer s.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go s.serve(c, e)
	}
}

func (s *Shim) serve(c net.Conn, e Endpoint) {
	defer c.Close()
	if reason := s.Down(); reason != "" {
		// A unix endpoint cannot carry the text to its client (ssh's agent
		// protocol, say): the terminal log is where it shows, rate-limited.
		msg := s.downMessage()
		s.logRefusal(msg)
		s.refuseText(c, e, msg)
		return
	}
	up, err := Dial(s.Sock, e.Service)
	if err != nil {
		var down *DownError
		var refused *RefusedError
		switch {
		case errors.As(err, &refused) && refused.Code == bridgewire.CodeVersion:
			// One side was upgraded alone: every stream fails this way, so the
			// bridge is unusable, not just this service (round 1, M5).
			s.MarkUnusable(err.Error())
			s.refuseText(c, e, err.Error())
		case errors.As(err, &down):
			// One failed dial fails THIS connection with the down text. It marks
			// the whole shim down only when `control` is not held: while the
			// control stream is open the server still sees the session, and only
			// its end means the bridge is gone (round 1, M2).
			if !s.controlHeld() {
				s.MarkDown(down.Reason)
			}
			s.logRefusal(bridgewire.DownMessage(down.Reason))
			s.refuseDown(c, e, down.Reason)
		default:
			// A refusal of THIS service: said to the client, the bridge stays up.
			s.logRefusal(fmt.Sprintf("clown: %v", err))
			s.refuseText(c, e, err.Error())
		}
		return
	}
	defer up.Close()
	pipe(c, up)
}

func (s *Shim) refuseDown(c net.Conn, e Endpoint, reason string) {
	s.refuseText(c, e, bridgewire.DownMessage(reason))
}

func (s *Shim) refuseText(c net.Conn, e Endpoint, text string) {
	if e.Network == "tcp" {
		_, _ = fmt.Fprintf(c, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(text), text)
	}
}

// Close stops the listeners and waits for the accept loops.
func (s *Shim) Close() {
	s.mu.Lock()
	lns := s.listeners
	s.listeners = nil
	s.mu.Unlock()
	for _, ln := range lns {
		_ = ln.Close()
	}
	s.wg.Wait()
}

// ControlRetryWindow and ControlRetryDelay: the first connection to `control`
// can land before the server can admit this unit (clown writes the unit's
// cgroup file once systemd reports it, a moment after the unit starts; the
// server answers `refused` to a peer it cannot yet identify) or while the server
// is still coming up. It is retried for this long, starting this far apart and
// doubling to a second, before the bridge is reported down. Retried: an
// unreachable bridge, `busy`, and `refused`. Final: every other refusal
// (`unknown-service`, `error`, `version`). The shim never half-closes `control`
// (CloseWrite): it writes nothing after the hello, and the stream's end is the
// signal, not ours.
var (
	ControlRetryWindow = 15 * time.Second
	ControlRetryDelay  = 100 * time.Millisecond
)

const controlRetryMaxDelay = time.Second

func dialControl(ctx context.Context, sock string) (net.Conn, error) {
	deadline := time.Now().Add(ControlRetryWindow)
	delay := ControlRetryDelay
	for {
		c, err := Dial(sock, bridgewire.ServiceControl)
		if err == nil {
			return c, nil
		}
		var down *DownError
		var refused *RefusedError
		retry := errors.As(err, &down) ||
			(errors.As(err, &refused) && (refused.Code == bridgewire.CodeBusy || refused.Code == bridgewire.CodeRefused))
		if !retry || time.Now().Add(delay).After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(delay):
		}
		delay = min(delay*2, controlRetryMaxDelay)
	}
}

// Frame is one NDJSON frame the server pushes on the control stream. Type says
// what it is ("restart", "stop", "turn", "idle", "wake", "bridge-down", ...);
// the rest of the object is the frame's own fields, kept raw.
type Frame struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// MaxFrame bounds one control frame.
const MaxFrame = 1 << 20

// Control opens the `control` stream and calls onFrame for each frame whose type
// is non-empty; a frame of a type the caller does not know is the caller's to
// ignore (the contract: unknown types are ignored, so the server can add new
// ones). It returns when the stream ends: nil error with the end of stream as
// the reason ("EOF on control"), or the dial error. ctx cancels it.
func Control(ctx context.Context, sock string, onFrame func(Frame)) (reason string, err error) {
	return ControlOpen(ctx, sock, nil, onFrame)
}

// ControlOpen is Control that calls onOpen once the stream is open (the server
// answered OK), so the caller knows `control` is held.
func ControlOpen(ctx context.Context, sock string, onOpen func(), onFrame func(Frame)) (reason string, err error) {
	c, err := dialControl(ctx, sock)
	if err != nil {
		return "", err
	}
	defer c.Close()
	go func() { <-ctx.Done(); _ = c.Close() }()
	if onOpen != nil {
		onOpen()
	}
	rd := bufio.NewReaderSize(c, 64<<10)
	var readErr error
	for {
		line, err := readFrameLine(rd)
		if err != nil {
			readErr = err
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f Frame
		if err := json.Unmarshal([]byte(line), &f); err != nil || f.Type == "" {
			continue // not a frame this version understands: ignored, like an unknown type
		}
		f.Raw = json.RawMessage(line)
		onFrame(f)
	}
	if ctx.Err() != nil {
		return "the shim was stopped", nil
	}
	if !errors.Is(readErr, io.EOF) {
		return "the control stream failed: " + shortErr(readErr), nil
	}
	return "EOF on control", nil
}

// readFrameLine reads one line. A line over MaxFrame is skipped (read to its
// newline and dropped, returned as an empty line): one over-long frame does not
// end the stream, so it cannot mark the bridge down. io.EOF ends the stream.
func readFrameLine(rd *bufio.Reader) (string, error) {
	var sb strings.Builder
	tooLong := false
	for {
		part, err := rd.ReadSlice('\n')
		if !tooLong {
			if sb.Len()+len(part) > MaxFrame {
				tooLong = true
				sb.Reset()
			} else {
				sb.Write(part)
			}
		}
		switch {
		case err == nil:
			if tooLong {
				return "", nil
			}
			return sb.String(), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return "", err
		}
	}
}

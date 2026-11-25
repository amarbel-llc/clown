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
	if e.Text == "" {
		return fmt.Sprintf("the bridge refused service %q: %s", e.Service, e.Code)
	}
	return fmt.Sprintf("the bridge refused service %q: %s: %s", e.Service, e.Code, e.Text)
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
	hello, err := bridgewire.Hello(service)
	if err != nil {
		return nil, err
	}
	if len(sock) > bridgewire.MaxSocketPath {
		return nil, &DownError{Reason: fmt.Sprintf("the socket path is %d bytes, over the unix limit of %d", len(sock), bridgewire.MaxSocketPath)}
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
	rep, err := bridgewire.ReadReply(c)
	if err != nil {
		c.Close()
		return nil, &DownError{Reason: "no answer to the hello: " + shortErr(err)}
	}
	if !rep.OK {
		c.Close()
		return nil, &RefusedError{Service: service, Code: rep.Code, Text: rep.Text}
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
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, c)
		close(done)
	}()
	_, _ = io.Copy(c, in)
	closeWrite(c)
	<-done
	return nil
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
	down      string // the reason, "" while up
	listeners []net.Listener
	wg        sync.WaitGroup
}

// MarkDown makes the shim answer every endpoint with the down text.
func (s *Shim) MarkDown(reason string) {
	s.mu.Lock()
	first := s.down == ""
	s.down = reason
	s.mu.Unlock()
	if first && s.Log != nil {
		fmt.Fprintln(s.Log, bridgewire.DownMessage(reason))
	}
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
		s.refuseDown(c, e, reason)
		return
	}
	up, err := Dial(s.Sock, e.Service)
	if err != nil {
		var down *DownError
		if errors.As(err, &down) {
			s.MarkDown(down.Reason)
			s.refuseDown(c, e, down.Reason)
			return
		}
		// A refusal of THIS service: said to the client, the bridge stays up.
		if s.Log != nil {
			fmt.Fprintf(s.Log, "clown: %v\n", err)
		}
		s.refuseText(c, e, err.Error())
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
	c, err := Dial(sock, bridgewire.ServiceControl)
	if err != nil {
		return "", err
	}
	defer c.Close()
	go func() { <-ctx.Done(); _ = c.Close() }()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64<<10), MaxFrame)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
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
	if err := sc.Err(); err != nil {
		return "the control stream failed: " + shortErr(err), nil
	}
	return "EOF on control", nil
}

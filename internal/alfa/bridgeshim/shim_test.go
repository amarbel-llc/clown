package bridgeshim

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
)

// fakeServer is a hermetic stand-in for the bridge: it speaks the contract's
// server side with services the test names. It enforces nothing else.
type fakeServer struct {
	sock string
	ln   net.Listener
	mu   sync.Mutex
	seen []string // the services of the hellos it accepted
}

// handler gets the connection after the OK was sent.
type handler func(c net.Conn)

func newFakeServer(t *testing.T, services map[string]handler) *fakeServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cbs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &fakeServer{sock: filepath.Join(dir, "bridge.sock")}
	s.ln, err = net.Listen("unix", s.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	go func() {
		for {
			c, err := s.ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, services)
		}
	}()
	return s
}

func (s *fakeServer) serve(c net.Conn, services map[string]handler) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(bridgewire.HelloTimeout))
	hello, err := bridgewire.ReadHello(c)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		if errors.Is(err, bridgewire.ErrMalformed) || errors.Is(err, bridgewire.ErrTooLong) {
			_, _ = c.Write(bridgewire.MalformedReply())
		}
		return
	}
	if hello.Major != bridgewire.Major {
		reply, _ := bridgewire.Decide(hello, nil)
		_, _ = c.Write(reply)
		return
	}
	svc := hello.Service
	h, ok := services[svc]
	if !ok {
		_, _ = c.Write(bridgewire.FormatErr(bridgewire.CodeUnknownService, svc))
		return
	}
	s.mu.Lock()
	s.seen = append(s.seen, svc)
	s.mu.Unlock()
	_, _ = c.Write(bridgewire.OK())
	h(c)
}

func (s *fakeServer) services() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.seen...)
}

func echo(c net.Conn) {
	_, _ = io.Copy(c, c)
}

func TestDialCarriesTheStreamBothWaysAndKeepsTheFirstServiceBytes(t *testing.T) {
	// The service writes immediately after OK: a reader that buffers the answer
	// would swallow "FIRST" (the proof of concept lost a wake line this way).
	srv := newFakeServer(t, map[string]handler{"svc": func(c net.Conn) {
		_, _ = io.WriteString(c, "FIRST\n")
		echo(c)
	}})
	c, err := Dial(srv.sock, "svc")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 6)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "FIRST\n" {
		t.Fatalf("first service bytes = %q, %v", buf, err)
	}
	_, _ = io.WriteString(c, "ping\n")
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping\n" {
		t.Errorf("echo = %q, %v", got, err)
	}
}

func TestDialMapsAnswersToErrors(t *testing.T) {
	srv := newFakeServer(t, map[string]handler{"good": echo})
	var re *RefusedError
	if _, err := Dial(srv.sock, "nope"); !errors.As(err, &re) || re.Code != bridgewire.CodeUnknownService || re.Service != "nope" {
		t.Errorf("unknown service: %v", err)
	}
	// A hello of another major, sent by hand (the shim only speaks its own): the
	// server's answer is the version line with its hint.
	c, err := net.Dial("unix", srv.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "BRIDGE/2 good\n")
	ok, code, text, err := bridgewire.ReadReply(c)
	if err != nil || ok || code != bridgewire.CodeVersion || !strings.Contains(text, "server=1 shim=2") {
		t.Errorf("version answer: %v %q %q, %v", ok, code, text, err)
	}
}

func TestAnUnreachableBridgeIsTheFixedDownText(t *testing.T) {
	_, err := Dial(filepath.Join(t.TempDir(), "none.sock"), "session")
	var down *DownError
	if !errors.As(err, &down) {
		t.Fatalf("want a *DownError, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "clown: the bridge is down (cannot connect to ") ||
		!strings.HasSuffix(err.Error(), "); no tool or model call can leave this session; the session's frontend is gone or restarting") {
		t.Errorf("down text: %q", err.Error())
	}
	// A socket path over the unix limit is refused up front, said plainly.
	long := "/" + strings.Repeat("d", 120) + "/bridge.sock"
	if _, err := Dial(long, "session"); err == nil || !strings.Contains(err.Error(), "over the unix limit") {
		t.Errorf("over-long path: %v", err)
	}
}

func TestAServerThatNeverAnswersIsDownNotAHang(t *testing.T) {
	// A listener that accepts and stays silent.
	dir, err := os.MkdirTemp("/tmp", "cbq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	old := DialTimeout
	DialTimeout = 300 * time.Millisecond
	defer func() { DialTimeout = old }()
	start := time.Now()
	_, err = Dial(sock, "session")
	var down *DownError
	if !errors.As(err, &down) || time.Since(start) > 3*time.Second {
		t.Errorf("a silent server: %v after %v", err, time.Since(start))
	}
}

func TestAnIllegalLabelNeverReachesTheServer(t *testing.T) {
	srv := newFakeServer(t, map[string]handler{"good": echo})
	for _, l := range []string{"", "Bad", strings.Repeat("a", 33), "a b"} {
		if _, err := Dial(srv.sock, l); err == nil {
			t.Errorf("%q must be refused", l)
		}
	}
	if got := srv.services(); len(got) != 0 {
		t.Errorf("the server saw %v", got)
	}
}

func TestStdioCarriesAnMCPStyleExchange(t *testing.T) {
	srv := newFakeServer(t, map[string]handler{"mcp-env": func(c net.Conn) {
		// One request line in, one response line out, then the client's EOF ends it.
		buf := make([]byte, 0, 64)
		one := make([]byte, 1)
		for {
			if _, err := c.Read(one); err != nil {
				return
			}
			buf = append(buf, one[0])
			if one[0] == '\n' {
				_, _ = c.Write(append([]byte("echo:"), buf...))
				buf = buf[:0]
			}
		}
	}})
	var out bytes.Buffer
	if err := Stdio(srv.sock, "mcp-env", strings.NewReader(`{"jsonrpc":"2.0","id":1}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "echo:"+`{"jsonrpc":"2.0","id":1}`+"\n" {
		t.Errorf("stdio output = %q", out.String())
	}
	if err := Stdio(srv.sock, "unknown", strings.NewReader("x"), io.Discard); err == nil {
		t.Error("an unknown service must be an error")
	}
}

func shortTCP(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestShimForwardsTCPAndUnixEndpointsAndAnswersWhenTheBridgeIsDown(t *testing.T) {
	srv := newFakeServer(t, map[string]handler{"model": echo, "keys": echo})
	var log bytes.Buffer
	sh := &Shim{Sock: srv.sock, Log: &log}
	tcp := shortTCP(t)
	dir, err := os.MkdirTemp("/tmp", "cbu")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	usock := filepath.Join(dir, "ssh.sock")
	if err := sh.Listen([]Endpoint{
		{Network: "tcp", Addr: tcp, Service: "model"},
		{Network: "unix", Addr: usock, Service: "keys"},
		{Network: "tcp", Addr: shortTCP(t), Service: "nothere"},
	}); err != nil {
		t.Fatal(err)
	}
	defer sh.Close()

	roundTrip := func(network, addr string) string {
		c, err := net.Dial(network, addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = io.WriteString(c, "hello\n")
		buf := make([]byte, 6)
		if _, err := io.ReadFull(c, buf); err != nil {
			return "ERR " + err.Error()
		}
		return string(buf)
	}
	if got := roundTrip("tcp", tcp); got != "hello\n" {
		t.Errorf("tcp endpoint: %q", got)
	}
	if got := roundTrip("unix", usock); got != "hello\n" {
		t.Errorf("unix endpoint: %q", got)
	}
	if sh.Down() != "" {
		t.Errorf("marked down early: %q", sh.Down())
	}

	// Now the bridge goes away: the next connection gets the fixed text as an
	// HTTP 503 on a tcp endpoint, a unix endpoint closes, and the log says it once.
	_ = srv.ln.Close()
	_ = os.Remove(srv.sock)
	c, err := net.Dial("tcp", tcp)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	body, _ := io.ReadAll(c)
	c.Close()
	if !strings.HasPrefix(string(body), "HTTP/1.1 503") || !strings.Contains(string(body), "the bridge is down (") {
		t.Errorf("down answer on a tcp endpoint: %q", body)
	}
	if sh.Down() == "" {
		t.Error("the shim did not mark itself down")
	}
	if got := roundTrip("unix", usock); !strings.HasPrefix(got, "ERR") {
		t.Errorf("a unix endpoint when down should close, read %q", got)
	}
	if n := strings.Count(log.String(), "the bridge is down ("); n != 1 {
		t.Errorf("the down text was logged %d times, want once:\n%s", n, log.String())
	}
}

func TestARefusedServiceIsSaidToTheClientAndTheBridgeStaysUp(t *testing.T) {
	srv := newFakeServer(t, map[string]handler{"model": echo})
	sh := &Shim{Sock: srv.sock}
	addr := shortTCP(t)
	if err := sh.Listen([]Endpoint{{Network: "tcp", Addr: addr, Service: "chat"}}); err != nil {
		t.Fatal(err)
	}
	defer sh.Close()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	body, _ := io.ReadAll(c)
	c.Close()
	if !strings.Contains(string(body), `refused service "chat": unknown-service`) {
		t.Errorf("refusal text: %q", body)
	}
	if sh.Down() != "" {
		t.Errorf("a refused SERVICE marked the bridge down: %q", sh.Down())
	}
}

func TestControlDeliversFramesIgnoresJunkAndEOFIsTheDownSignal(t *testing.T) {
	srv := newFakeServer(t, map[string]handler{"control": func(c net.Conn) {
		_, _ = io.WriteString(c, `{"type":"wake","n":1}`+"\n")
		_, _ = io.WriteString(c, "not json\n")
		_, _ = io.WriteString(c, `{"nottype":1}`+"\n")
		_, _ = io.WriteString(c, `{"type":"a-type-from-the-future","x":[1,2]}`+"\n")
		_, _ = io.WriteString(c, "\n")
		_, _ = io.WriteString(c, `{"type":"restart"}`+"\n")
		// the handler returns: the server closes the stream = EOF on control
	}})
	var types []string
	reason, err := Control(context.Background(), srv.sock, func(f Frame) { types = append(types, f.Type) })
	if err != nil {
		t.Fatal(err)
	}
	if reason != "EOF on control" {
		t.Errorf("reason = %q", reason)
	}
	if strings.Join(types, ",") != "wake,a-type-from-the-future,restart" {
		t.Errorf("frames delivered: %v (junk and typeless lines are ignored, unknown types are delivered for the caller to ignore)", types)
	}
	if bridgewire.DownMessage(reason) != "clown: the bridge is down (EOF on control); no tool or model call can leave this session; the session's frontend is gone or restarting" {
		t.Error("the down text for EOF on control")
	}
}

// The first connection to control can come before the server has the cgroup
// (it closes the stream, or answers busy): it is retried, a bounded number of
// times, and a final refusal of the service is not.
func TestControlRetriesAnUnreadyServerThenGivesUp(t *testing.T) {
	oldN, oldD := ControlAttempts, ControlRetryDelay
	ControlAttempts, ControlRetryDelay = 3, 10*time.Millisecond
	t.Cleanup(func() { ControlAttempts, ControlRetryDelay = oldN, oldD })

	var mu sync.Mutex
	var hellos int
	dir, err := os.MkdirTemp("/tmp", "cbr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "bridge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	readyAfter := 3 // the third hello is served
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if _, err := bridgewire.ReadHello(c); err != nil {
					return
				}
				mu.Lock()
				hellos++
				n, ra := hellos, readyAfter
				mu.Unlock()
				switch {
				case n < ra-1:
					// not ready: close with no answer (the serve's peer check failing)
				case n == ra-1:
					_, _ = c.Write(bridgewire.FormatErr(bridgewire.CodeBusy, "starting"))
				default:
					_, _ = c.Write(bridgewire.OK())
					_, _ = io.WriteString(c, `{"type":"wake"}`+"\n")
				}
			}()
		}
	}()
	var types []string
	reason, err := Control(context.Background(), sock, func(f Frame) { types = append(types, f.Type) })
	if err != nil || reason != "EOF on control" || strings.Join(types, ",") != "wake" {
		t.Fatalf("after the retries: reason %q, err %v, frames %v", reason, err, types)
	}
	mu.Lock()
	got := hellos
	mu.Unlock()
	if got != 3 {
		t.Errorf("the server saw %d hellos, want 3 (two refused, one served)", got)
	}

	// Never ready: gives up after exactly ControlAttempts and reports the last error.
	mu.Lock()
	hellos, readyAfter = 0, 1000
	mu.Unlock()
	if _, err := Control(context.Background(), sock, func(Frame) {}); err == nil {
		t.Error("a server that never becomes ready must be an error")
	}
	mu.Lock()
	got = hellos
	mu.Unlock()
	if got != 3 {
		t.Errorf("the server saw %d hellos on a never-ready server, want ControlAttempts = 3", got)
	}

	// A refusal of the service itself is final: one hello, no retry.
	srv := newFakeServer(t, map[string]handler{})
	if _, err := Control(context.Background(), srv.sock, func(Frame) {}); err == nil {
		t.Error("an unknown service must be an error")
	}
}

func TestControlStopsWithItsContextAndReportsADialFailure(t *testing.T) {
	srv := newFakeServer(t, map[string]handler{"control": func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		r, _ := Control(ctx, srv.sock, func(Frame) {})
		done <- r
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case r := <-done:
		if r != "the shim was stopped" {
			t.Errorf("reason on cancel = %q", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Control did not stop with its context")
	}
	if _, err := Control(context.Background(), filepath.Join(t.TempDir(), "none"), func(Frame) {}); err == nil {
		t.Error("a dial failure must be an error")
	}
}

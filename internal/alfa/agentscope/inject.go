package agentscope

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ModelPort is the in-unit loopback port the model injection proxy is relayed
// from (clown#336): beside ServePortBase and BrokerPort, below InUnitPortBase.
const ModelPort = BrokerPort + 1

// ModelPlaceholder is the only model credential a unit holds when the proxy is
// on: a fixed, obviously fake string, so a transcript line that quotes it reads
// as such and no secret scanner fires on it.
const ModelPlaceholder = "clown-placeholder-not-a-credential"

// Result.ModelCredential values: which path a unit got its model credential by.
const (
	ModelCredentialProxy     = "proxy"
	ModelCredentialLoginFile = "login-file"
)

// DefaultModelUpstream is where the proxy forwards.
const DefaultModelUpstream = "https://api.anthropic.com"

// ModelAddr is the in-unit address the proxy is relayed from; the unit's
// ANTHROPIC_BASE_URL names it.
func ModelAddr() string { return "127.0.0.1:" + strconv.Itoa(ModelPort) }

// modelAPIHosts are the hosts a unit needs only for its own model traffic and
// login: with the proxy on, all of it leaves from the frontend.
var modelAPIHosts = map[string]bool{
	"api.anthropic.com:443": true, "console.anthropic.com:443": true,
	"platform.claude.com:443": true, "claude.ai:443": true,
}

// WithoutModelAPIHosts is a copy of a without the model API and login hosts.
func WithoutModelAPIHosts(a Allowlist) Allowlist {
	out := make(Allowlist, 0, len(a))
	for _, e := range a {
		if !modelAPIHosts[strings.ToLower(e)] {
			out = append(out, e)
		}
	}
	return out
}

// InjectProxy is the frontend-scope reverse proxy for the model API of one
// agent unit (clown#336). It serves HTTP on a unix socket bound into the unit
// (relayed there as http://127.0.0.1:ModelPort), admits only callers in the
// unit's cgroup, forwards three request shapes to Upstream and replaces the
// Authorization header with the bearer Token returns. The unit holds only
// ModelPlaceholder; the real value is read per request and appears in no log,
// error or response.
//
// Everything else a request carries, anthropic-beta included, is relayed
// untouched: the beta list differs per run and per claude version.
type InjectProxy struct {
	// Unit is the agent unit's name; callers must be in .../clown-agent.slice/<Unit>.
	Unit string
	// Token returns the bearer for one request (TokenSource.Token).
	Token func() (string, error)
	// Upstream is the API origin; nil means DefaultModelUpstream. It carries no path.
	Upstream *url.URL
	// Transport carries the upstream calls; nil means a TLS transport from the
	// frontend's own network namespace. Tests replace it.
	Transport http.RoundTripper
	Logger    *slog.Logger
	// PeerCgroup reads a caller's cgroup from its pid; tests replace it.
	PeerCgroup func(pid int) (string, error)
	// Permissive forwards any path under /v1/ or /api/, logged: for the next
	// probe of a new claude version (CLOWN_MODEL_PROXY_PERMISSIVE), not a knob.
	Permissive bool
	// OnRejected is called once, on the first 401 or 403 from the upstream.
	OnRejected func(status int)
	// Stderr receives the one-line rejection notice; nil means os.Stderr.
	Stderr io.Writer

	mu         sync.Mutex
	srv        *http.Server
	rejectOnce sync.Once
}

// Listen creates the socket at path (0600) and serves it until Close.
func (p *InjectProxy) Listen(path string) error {
	up := p.Upstream
	if up == nil {
		var err error
		if up, err = url.Parse(DefaultModelUpstream); err != nil {
			return err
		}
	}
	if up.Scheme == "" || up.Host == "" || (up.Path != "" && up.Path != "/") {
		return fmt.Errorf("model upstream %q must be a scheme and host without a path", up.Redacted())
	}
	ln, err := listenUnix(path)
	if err != nil {
		return err
	}
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme, req.URL.Host, req.Host = up.Scheme, up.Host, up.Host
			// A present, nil entry stops ReverseProxy adding the caller's address.
			req.Header["X-Forwarded-For"] = nil
		},
		Transport:     p.transport(),
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				p.noticeRejection(resp.StatusCode)
			}
			return nil
		},
		ErrorLog: slog.NewLogLogger(discardHandler{}, slog.LevelError),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log(slog.LevelWarn, "inject: upstream error", "method", r.Method, "path", r.URL.Path, "err", err)
			http.Error(w, "model API unreachable", http.StatusBadGateway)
		},
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.serve(rp, w, r) }),
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          slog.NewLogLogger(discardHandler{}, slog.LevelError),
	}
	p.mu.Lock()
	p.srv = srv
	p.mu.Unlock()
	go srv.Serve(&unitListener{Listener: ln, p: p})
	return nil
}

// Close stops accepting and drops open connections.
func (p *InjectProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

func (p *InjectProxy) transport() http.RoundTripper {
	if p.Transport != nil {
		return p.Transport
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0, // a model call may think for minutes before its first byte
	}
}

// unitListener admits only connections whose peer is in the unit's cgroup,
// before any byte is parsed.
type unitListener struct {
	net.Listener
	p *InjectProxy
}

func (l *unitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if err := authorizeUnitPeer(c, l.p.Unit, l.p.PeerCgroup); err != nil {
			l.p.log(slog.LevelWarn, "inject: refused caller", "err", err)
			c.Close()
			continue
		}
		return c, nil
	}
}

// serve checks the allowlist, injects the credential, and hands the request to
// the reverse proxy.
func (p *InjectProxy) serve(rp *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w}
	defer func() {
		p.log(slog.LevelInfo, "inject: request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status(), "bytes", rec.bytes, "elapsed", time.Since(start).Round(time.Millisecond))
	}()
	if !p.allowed(r) {
		p.log(slog.LevelWarn, "inject: refused request", "method", r.Method, "path", r.URL.Path)
		http.Error(rec, "not a model API request clown forwards: "+r.Method+" "+r.URL.Path, http.StatusForbidden)
		return
	}
	if r.Method == http.MethodHead && r.URL.Path == "/api/hello" {
		r.Header.Del("Authorization")
	} else {
		tok, err := p.token()
		if err != nil {
			p.log(slog.LevelWarn, "inject: no usable model credential", "err", err)
			http.Error(rec, "model credential unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	rp.ServeHTTP(rec, r)
}

func (p *InjectProxy) token() (string, error) {
	if p.Token == nil {
		return "", fmt.Errorf("no token source")
	}
	return p.Token()
}

// allowed is the path and method allowlist, checked before any credential is
// read. A path whose escaped form differs from its decoded one is refused, so
// what is checked is what is forwarded.
func (p *InjectProxy) allowed(r *http.Request) bool {
	path := r.URL.Path
	if r.URL.RawPath == "" || r.URL.RawPath == path {
		switch {
		case r.Method == http.MethodHead && path == "/api/hello":
			return true
		case r.Method == http.MethodPost && (path == "/v1/messages" || path == "/v1/messages/count_tokens"):
			return true
		}
	}
	if p.Permissive && (strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/api/")) {
		p.log(slog.LevelWarn, "inject: permissive pass", "method", r.Method, "path", path)
		return true
	}
	return false
}

func (p *InjectProxy) noticeRejection(status int) {
	p.rejectOnce.Do(func() {
		p.log(slog.LevelWarn, "model credential rejected; rotate the entry and run piggy secrets reconcile", "unit", p.Unit, "status", status)
		out := p.Stderr
		if out == nil {
			out = os.Stderr
		}
		fmt.Fprintf(out, "clown: model credential rejected by the API (status %d) for %s; rotate the entry and run piggy secrets reconcile\n", status, p.Unit)
		if p.OnRejected != nil {
			p.OnRejected(status)
		}
	})
}

func (p *InjectProxy) log(level slog.Level, msg string, args ...any) {
	if p.Logger != nil {
		p.Logger.Log(context.Background(), level, msg, args...)
	}
}

// discardHandler silences the standard-library loggers net/http and
// ReverseProxy would otherwise write to the frontend's stderr.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }

// statusRecorder notes the status and byte count of a response, and passes
// flushes through so SSE reaches the unit event by event.
type statusRecorder struct {
	http.ResponseWriter
	code  int
	bytes int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) status() int {
	if s.code == 0 {
		return http.StatusOK
	}
	return s.code
}

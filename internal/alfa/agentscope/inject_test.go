package agentscope

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testBearer = "clown-test-bearer-not-real"

// lockedBuffer is a log sink the proxy goroutines and the test share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// injectRig is an InjectProxy in front of a fake TLS API.
type injectRig struct {
	proxy    *InjectProxy
	client   *http.Client
	logs     *lockedBuffer
	upstream *httptest.Server
	hits     atomic.Int64
	rejected atomic.Int64
}

// newInjectRig serves handler as the fake API behind a proxy that injects
// testBearer for the unit. mutate adjusts the proxy before it listens.
func newInjectRig(t *testing.T, handler http.HandlerFunc, mutate func(*InjectProxy)) *injectRig {
	t.Helper()
	rig := &injectRig{logs: &lockedBuffer{}}
	rig.upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(rig.upstream.Close)
	u, err := url.Parse(rig.upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	rig.proxy = &InjectProxy{
		Unit:       "u-1.service",
		Token:      func() (string, error) { return testBearer, nil },
		Upstream:   u,
		Transport:  rig.upstream.Client().Transport,
		Logger:     slog.New(slog.NewTextHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		PeerCgroup: func(int) (string, error) { return unitCgroup, nil },
		OnRejected: func(int) { rig.rejected.Add(1) },
	}
	if mutate != nil {
		mutate(rig.proxy)
	}
	sock := filepath.Join(t.TempDir(), "model.sock")
	if err := rig.proxy.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rig.proxy.Close() })
	rig.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	return rig
}

// do sends a request as the unit's claude would: plain HTTP to the proxy
// with the placeholder bearer.
func (r *injectRig) do(t *testing.T, method, path string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://model.test"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hdr["Authorization"]; !ok && path != "/api/hello" {
		req.Header.Set("Authorization", "Bearer "+ModelPlaceholder)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func drain(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInjectReplacesTheBearerAndRelaysEverythingElseUntouched(t *testing.T) {
	var seen *http.Request
	var seenBody []byte
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {
		seen, seenBody = r, nil
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, nil)
	beta := "claude-code-20250219,oauth-2025-04-20,some-future-beta-2027-01-01"
	resp := rig.do(t, http.MethodPost, "/v1/messages?beta=true", strings.NewReader(`{"model":"m"}`), map[string]string{
		"Anthropic-Beta":    beta,
		"Anthropic-Version": "2023-06-01",
		"X-App":             "cli",
		"User-Agent":        "claude-cli/test",
		"Content-Type":      "application/json",
	})
	if got := drain(t, resp); resp.StatusCode != 200 || got != `{"ok":true}` {
		t.Fatalf("status %d body %q", resp.StatusCode, got)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer "+testBearer {
		t.Errorf("upstream Authorization = %q, want the injected bearer", got)
	}
	for k, vs := range seen.Header {
		for _, v := range vs {
			if strings.Contains(v, ModelPlaceholder) {
				t.Errorf("the placeholder reached the upstream in %s: %q", k, v)
			}
		}
	}
	for k, want := range map[string]string{"Anthropic-Beta": beta, "Anthropic-Version": "2023-06-01", "X-App": "cli", "User-Agent": "claude-cli/test", "Content-Type": "application/json"} {
		if got := seen.Header.Get(k); got != want {
			t.Errorf("upstream %s = %q, want %q (relayed untouched)", k, got, want)
		}
	}
	if seen.URL.RawQuery != "beta=true" || seen.URL.Path != "/v1/messages" || seen.Method != http.MethodPost {
		t.Errorf("upstream saw %s %s?%s", seen.Method, seen.URL.Path, seen.URL.RawQuery)
	}
	if string(seenBody) != `{"model":"m"}` {
		t.Errorf("upstream body %q", seenBody)
	}
	if seen.Host != strings.TrimPrefix(rig.upstream.URL, "https://") {
		t.Errorf("upstream Host = %q, want the upstream's", seen.Host)
	}
	if seen.Header.Get("X-Forwarded-For") != "" {
		t.Errorf("X-Forwarded-For set: %q", seen.Header.Get("X-Forwarded-For"))
	}
}

func TestInjectFlushesAStreamedResponsePerEvent(t *testing.T) {
	release := make(chan struct{})
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		_, _ = io.WriteString(w, "data: two\n\n")
	}, nil)
	resp := rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil)
	defer resp.Body.Close()
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	select {
	case l := <-lines:
		if l != "data: one" {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first event was not flushed before the response ended: the proxy buffers")
	}
	close(release)
	select {
	case l := <-lines:
		if l != "" {
			t.Fatalf("second line %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stalled after the release")
	}
}

// patternReader yields n bytes of a repeating pattern, so a body larger than
// any buffer needs no memory on the test's side.
type patternReader struct {
	n   int64
	off int64
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > p.n {
		b = b[:p.n]
	}
	for i := range b {
		b[i] = byte((p.off + int64(i)) % 251)
	}
	p.off += int64(len(b))
	p.n -= int64(len(b))
	return len(b), nil
}

func TestInjectStreamsALargeRequestBody(t *testing.T) {
	const size = 24 << 20
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {
		h := sha256.New()
		n, _ := io.Copy(h, r.Body)
		fmt.Fprintf(w, "%d %s", n, hex.EncodeToString(h.Sum(nil)))
	}, nil)
	want := sha256.New()
	_, _ = io.Copy(want, &patternReader{n: size})
	resp := rig.do(t, http.MethodPost, "/v1/messages", &patternReader{n: size}, nil)
	if got, wantS := drain(t, resp), fmt.Sprintf("%d %s", size, hex.EncodeToString(want.Sum(nil))); got != wantS {
		t.Fatalf("upstream received %q, want %q", got, wantS)
	}
}

func TestInjectRefusesPathsAndMethodsOffTheAllowlist(t *testing.T) {
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "reached") }, nil)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/messages"},
		{"HEAD", "/v1/messages"},
		{"POST", "/v1/messages/"},
		{"POST", "/v1/messages/count_tokens/x"},
		{"POST", "/v1/models"},
		{"GET", "/v1/models"},
		{"POST", "/v1/organizations/x/usage"},
		{"POST", "/api/hello"},
		{"GET", "/api/hello"},
		{"HEAD", "/api/hello/x"},
		{"POST", "//v1/messages"},
		{"POST", "/v1/../etc"},
		{"POST", "/"},
		{"DELETE", "/v1/messages"},
	} {
		resp := rig.do(t, tc.method, tc.path, nil, nil)
		body := drain(t, resp)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "reached") {
			t.Errorf("%s %s: status %d, want 403 from the proxy", tc.method, tc.path, resp.StatusCode)
		}
	}
	if n := rig.hits.Load(); n != 0 {
		t.Errorf("%d refused requests reached the upstream", n)
	}
	logs := rig.logs.String()
	for _, want := range []string{"/v1/models", "/api/hello", "refused"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log does not mention %q:\n%s", want, logs)
		}
	}
}

func TestInjectForwardsHelloWithoutAuthorizationAndCountTokensWithIt(t *testing.T) {
	auth := map[string]string{}
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {
		auth[r.Method+" "+r.URL.Path] = r.Header.Get("Authorization")
	}, nil)
	// Even if the caller sent one, hello goes out without.
	drain(t, rig.do(t, http.MethodHead, "/api/hello", nil, map[string]string{"Authorization": "Bearer " + ModelPlaceholder}))
	drain(t, rig.do(t, http.MethodPost, "/v1/messages/count_tokens", strings.NewReader("{}"), nil))
	if got, ok := auth["HEAD /api/hello"]; !ok || got != "" {
		t.Errorf("HEAD /api/hello carried Authorization %q (reached: %v)", got, ok)
	}
	if got := auth["POST /v1/messages/count_tokens"]; got != "Bearer "+testBearer {
		t.Errorf("count_tokens Authorization = %q", got)
	}
}

func TestInjectPassesAnUpstream401ThroughAndNoticesOnce(t *testing.T) {
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid"}`)
	}, nil)
	for i := 0; i < 3; i++ {
		resp := rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil)
		if body := drain(t, resp); resp.StatusCode != http.StatusUnauthorized || body != `{"error":"invalid"}` {
			t.Fatalf("round %d: %d %q, want the upstream's 401 unchanged", i, resp.StatusCode, body)
		}
	}
	if n := rig.hits.Load(); n != 3 {
		t.Errorf("%d upstream calls for 3 requests: the proxy retried or dropped", n)
	}
	if n := rig.rejected.Load(); n != 1 {
		t.Errorf("OnRejected called %d times, want once per proxy", n)
	}
	if n := strings.Count(rig.logs.String(), "model credential rejected"); n != 1 {
		t.Errorf("rejection notice logged %d times, want once:\n%s", n, rig.logs.String())
	}
}

func TestInjectServesOnlyTheUnit(t *testing.T) {
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "reached") }, func(p *InjectProxy) {
		p.PeerCgroup = func(int) (string, error) { return "/user.slice/user-1001.slice/session-3.scope", nil }
	})
	req, _ := http.NewRequest(http.MethodPost, "http://model.test/v1/messages", strings.NewReader("{}"))
	if resp, err := rig.client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("a caller outside the unit got status %d", resp.StatusCode)
	}
	if n := rig.hits.Load(); n != 0 {
		t.Errorf("%d requests from outside the unit reached the upstream", n)
	}
	if !strings.Contains(rig.logs.String(), "refused caller") {
		t.Errorf("no refusal logged:\n%s", rig.logs.String())
	}
}

func TestInjectWithoutATokenAnswers503AndCallsNoUpstream(t *testing.T) {
	var fail atomic.Bool
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Header.Get("Authorization")) }, func(p *InjectProxy) {
		calls := 0
		p.Token = func() (string, error) {
			calls++
			if fail.Load() {
				return "", errors.New("no token at /x: absent")
			}
			return fmt.Sprintf("clown-test-bearer-%d", calls), nil
		}
	})
	// The token is read per request: a new value is the next request's.
	for i, want := range []string{"Bearer clown-test-bearer-1", "Bearer clown-test-bearer-2"} {
		resp := rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil)
		if got := drain(t, resp); got != want {
			t.Fatalf("request %d upstream Authorization = %q, want %q", i, got, want)
		}
	}
	fail.Store(true)
	hits := rig.hits.Load()
	resp := rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil)
	if body := drain(t, resp); resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(body, "Bearer") {
		t.Fatalf("revoked token: %d %q, want 503", resp.StatusCode, body)
	}
	if rig.hits.Load() != hits {
		t.Error("a request with no token reached the upstream")
	}
	// hello needs no token and keeps working.
	if resp := rig.do(t, http.MethodHead, "/api/hello", nil, nil); resp.StatusCode != 200 {
		t.Errorf("hello without a token: %d", resp.StatusCode)
	}
}

func TestInjectPermissiveForwardsOtherPathsUnderV1AndAPIAndLogsThem(t *testing.T) {
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "reached") }, func(p *InjectProxy) {
		p.Permissive = true
	})
	if resp := rig.do(t, http.MethodGet, "/v1/models", nil, nil); drain(t, resp) != "reached" {
		t.Error("permissive mode refused /v1/models")
	}
	if resp := rig.do(t, http.MethodPost, "/api/other", strings.NewReader("{}"), nil); drain(t, resp) != "reached" {
		t.Error("permissive mode refused /api/other")
	}
	if resp := rig.do(t, http.MethodGet, "/etc/passwd", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("permissive mode forwarded /etc/passwd: %d", resp.StatusCode)
	}
	if !strings.Contains(rig.logs.String(), "/v1/models") {
		t.Errorf("a permissive pass is not logged:\n%s", rig.logs.String())
	}
}

func TestInjectAnUpstreamFailureIs502WithoutDetail(t *testing.T) {
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {}, func(p *InjectProxy) {
		p.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: connection refused") })
	})
	resp := rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil)
	if body := drain(t, resp); resp.StatusCode != http.StatusBadGateway || strings.Contains(body, testBearer) {
		t.Fatalf("%d %q, want 502", resp.StatusCode, body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestInjectNeverLogsOrReturnsTheToken is the canary: a token from a real
// token file goes through the success path, the 401 path, an upstream
// failure, a refused path, a refused peer and a revoked file, and appears in
// no log line and no response the client sees, nor in the launch's result
// file or systemd-run argv.
func TestInjectNeverLogsOrReturnsTheToken(t *testing.T) {
	path := writeTokenFile(t, canaryToken+"\n", 0o600)
	status := atomic.Int64{}
	status.Store(200)
	var down atomic.Bool
	rig := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		io.WriteString(w, "upstream body")
	}, func(p *InjectProxy) {
		p.Token = TokenSource{Path: path}.Token
		base := p.Transport
		p.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if down.Load() {
				return nil, errors.New("upstream down")
			}
			return base.RoundTrip(r)
		})
	})
	var responses []string
	record := func(resp *http.Response) { responses = append(responses, drain(t, resp)) }
	record(rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil))
	status.Store(401)
	record(rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil))
	status.Store(403)
	record(rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil))
	record(rig.do(t, http.MethodPost, "/v1/models", nil, nil))
	down.Store(true)
	record(rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	record(rig.do(t, http.MethodPost, "/v1/messages", strings.NewReader("{}"), nil))
	// The same proxy type refusing a peer, with its log sink captured too.
	refused := newInjectRig(t, func(w http.ResponseWriter, r *http.Request) {}, func(p *InjectProxy) {
		p.Token = TokenSource{Path: writeTokenFile(t, canaryToken, 0o600)}.Token
		p.PeerCgroup = func(int) (string, error) { return "/user.slice/x.scope", nil }
	})
	req, _ := http.NewRequest(http.MethodPost, "http://model.test/v1/messages", strings.NewReader("{}"))
	if resp, err := refused.client.Do(req); err == nil {
		resp.Body.Close()
	}

	sinks := map[string]string{"proxy log": rig.logs.String(), "refused-peer log": refused.logs.String(), "responses": strings.Join(responses, "\n")}
	l := Launch{
		Spec: validSpec(), Unit: "u-1.service", WorkDir: validSpec().Worktree, AgentHome: "/h", Argv: []string{"claude"},
		EgressSocket: "/run/x/egress.sock", Model: Relay{Listen: ModelAddr(), Socket: "/run/x/model.sock"}, ModelCredential: ModelCredentialProxy,
	}
	args, err := l.SystemdRunArgs()
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "result.json")
	if err := WriteResult(resultPath, l.baseResult()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(resultPath)
	sinks["systemd-run argv"] = strings.Join(args, "\n")
	sinks["result file"] = string(raw)
	for name, text := range sinks {
		if strings.Contains(text, canaryToken) {
			t.Errorf("the token appears in the %s:\n%s", name, text)
		}
	}
	if !strings.Contains(sinks["proxy log"], "/v1/messages") {
		t.Fatalf("the proxy logged nothing; the canary test proves nothing:\n%s", sinks["proxy log"])
	}
}

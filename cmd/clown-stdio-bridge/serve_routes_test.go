package main

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// clown#301: on the serve socket the control routes (/clown/system-prompt,
// /clown/exclude-tools) are the frontend's only; the unit reaches /mcp and
// /healthz but cannot clear its own exclusions.

const testUnitCgroup = "/user.slice/user-1001.slice/user@1001.service/clown.slice/clown-agent.slice/u-1.service"

// fakePromptChild answers the bridge's prompts/get with one text message and
// ignores everything else, standing in for a wrapped stdio child.
func fakePromptChild(t *testing.T) *translator {
	t.Helper()
	childIn, bridgeOut := io.Pipe() // bridge writes the child's stdin
	bridgeIn, childOut := io.Pipe() // the child writes its stdout
	go func() {
		sc := bufio.NewScanner(childIn)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"prompts/get"`) {
				_, _ = io.WriteString(childOut, `{"jsonrpc":"2.0","id":"clown-system-prompt","result":{"messages":[{"role":"user","content":{"type":"text","text":"fragment"}}]}}`+"\n")
			}
		}
	}()
	tr := newTranslator(bridgeOut, bridgeIn, log.New(io.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); bridgeOut.Close(); childOut.Close() })
	go func() { _ = tr.Run(ctx) }()
	return tr
}

// serveFor starts the bridge's mux on a serve socket whose single caller (this
// test process) is classed frontend or unit, and returns a client for it.
func serveFor(t *testing.T, frontend bool) *http.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "serve", "p1", "spinclass.sock")
	frontendPID := func() int { return 0 }
	if frontend {
		frontendPID = os.Getpid
	}
	ln, err := agentscope.ListenServeSocket(path, "u-1.service", frontendPID, func(int) (string, error) { return testUnitCgroup, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := fakePromptChild(t)
	h := newHTTPHandler(tr, log.New(io.Discard, "", 0), nil)
	srv := &http.Server{Handler: newMux(h, tr, make(chan error), true), ConnContext: serveConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
}

func status(t *testing.T, c *http.Client, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, "http://unix"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestServeSocketControlRoutesAreForTheFrontendOnly(t *testing.T) {
	unit := serveFor(t, false)
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/clown/exclude-tools", ""},
		{http.MethodPost, "/clown/exclude-tools", `{"exclude":[]}`},
		{http.MethodGet, "/clown/system-prompt", ""},
	} {
		if got := status(t, unit, r.method, r.path, r.body); got != http.StatusForbidden {
			t.Errorf("unit %s %s = %d, want 403", r.method, r.path, got)
		}
	}
	// The unit still speaks MCP and can probe health.
	if got := status(t, unit, http.MethodPost, "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); got != http.StatusAccepted {
		t.Errorf("unit POST /mcp notification = %d, want 202", got)
	}
	if got := status(t, unit, http.MethodGet, "/healthz", ""); got != http.StatusOK {
		t.Errorf("unit GET /healthz = %d, want 200", got)
	}

	frontend := serveFor(t, true)
	if got := status(t, frontend, http.MethodPost, "/clown/exclude-tools", `{"exclude":["x"]}`); got != http.StatusOK {
		t.Errorf("frontend POST /clown/exclude-tools = %d, want 200 (cheap-context push)", got)
	}
	if got := status(t, frontend, http.MethodGet, "/clown/exclude-tools", ""); got != http.StatusOK {
		t.Errorf("frontend GET /clown/exclude-tools = %d, want 200 (readback)", got)
	}
	if got := status(t, frontend, http.MethodGet, "/clown/system-prompt", ""); got != http.StatusOK {
		t.Errorf("frontend GET /clown/system-prompt = %d, want 200", got)
	}
	if got := status(t, frontend, http.MethodGet, "/healthz", ""); got != http.StatusOK {
		t.Errorf("frontend GET /healthz = %d, want 200", got)
	}
}

// The loopback (non-serve) bridge is unchanged: every route answers.
func TestLoopbackBridgeKeepsItsControlRoutes(t *testing.T) {
	tr := fakePromptChild(t)
	h := newHTTPHandler(tr, log.New(io.Discard, "", 0), nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: newMux(h, tr, make(chan error), false)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	c := &http.Client{Timeout: 10 * time.Second}
	for _, path := range []string{"/clown/exclude-tools", "/clown/system-prompt", "/healthz"} {
		resp, err := c.Get("http://" + ln.Addr().String() + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("loopback GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
}

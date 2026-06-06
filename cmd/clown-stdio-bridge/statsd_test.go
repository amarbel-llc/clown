package main

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSanitizeMetric(t *testing.T) {
	tests := []struct{ in, want string }{
		{"tools/call", "tools_call"},
		{"ci-run-get", "ci-run-get"},
		{"a.b:c|d e", "a_b_c_d_e"},
		{"Already_OK-123", "Already_OK-123"},
	}
	for _, tc := range tests {
		if got := sanitizeMetric(tc.in); got != tc.want {
			t.Errorf("sanitizeMetric(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMetricLabel(t *testing.T) {
	toolsCall := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ci-run-get","arguments":{}}}`)
	if got := metricLabel("tools/call", toolsCall); got != "ci-run-get" {
		t.Errorf("tools/call label = %q, want tool name", got)
	}
	// tools/call without a name falls back to the sanitized method.
	if got := metricLabel("tools/call", []byte(`{"method":"tools/call"}`)); got != "tools_call" {
		t.Errorf("nameless tools/call label = %q, want tools_call", got)
	}
	if got := metricLabel("initialize", []byte(`{}`)); got != "initialize" {
		t.Errorf("initialize label = %q", got)
	}
}

func TestResponseIsError(t *testing.T) {
	if responseIsError(json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`)) {
		t.Error("result response classified as error")
	}
	if !responseIsError(json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`)) {
		t.Error("error response not classified as error")
	}
	if responseIsError(json.RawMessage(`{"id":1,"error":null}`)) {
		t.Error("error:null classified as error")
	}
	if responseIsError(json.RawMessage(`not json`)) {
		t.Error("unparseable response classified as error")
	}
}

// statsdListener binds a local UDP socket and points the statsd env vars at
// it, returning a function that reads the next datagram (or "" on timeout).
func statsdListener(t *testing.T) func() string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	host, port, err := net.SplitHostPort(pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(statsdHostEnvVar, host)
	t.Setenv(statsdPortEnvVar, port)
	return func() string {
		buf := make([]byte, 1024)
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return ""
		}
		return string(buf[:n])
	}
}

func TestStatsdClientEmitsLineProtocol(t *testing.T) {
	next := statsdListener(t)
	t.Setenv(statsdDisableEnvVar, "")
	t.Setenv(serverNameEnvVar, "mock-mcp")

	c := newStatsdFromEnv()
	if c == nil {
		t.Fatal("client disabled despite listener + no disable flag")
	}

	c.timing("ci-run-get.duration", 1500*time.Millisecond)
	if got := next(); got != "clown.bridge.mock-mcp.ci-run-get.duration:1500|ms" {
		t.Errorf("timing datagram = %q", got)
	}

	c.incr("ci-run-get.success")
	if got := next(); got != "clown.bridge.mock-mcp.ci-run-get.success:1|c" {
		t.Errorf("counter datagram = %q", got)
	}
}

func TestStatsdEmitOutcome(t *testing.T) {
	next := statsdListener(t)
	t.Setenv(statsdDisableEnvVar, "")
	t.Setenv(serverNameEnvVar, "s")
	c := newStatsdFromEnv()

	started := time.Now().Add(-25 * time.Millisecond)
	c.emitOutcome("tool", started, "success")
	first := next()
	if !strings.HasPrefix(first, "clown.bridge.s.tool.duration:") || !strings.HasSuffix(first, "|ms") {
		t.Errorf("want duration before success counter, got %q", first)
	}
	if got := next(); got != "clown.bridge.s.tool.success:1|c" {
		t.Errorf("success counter = %q", got)
	}

	// Abandoned: counter only, no duration.
	c.emitOutcome("tool", started, "abandoned")
	if got := next(); got != "clown.bridge.s.tool.abandoned:1|c" {
		t.Errorf("abandoned counter = %q (a duration here means abandoned wrongly emitted a timer)", got)
	}
}

func TestStatsdDisabledAndNilSafe(t *testing.T) {
	t.Setenv(statsdDisableEnvVar, "1")
	c := newStatsdFromEnv()
	if c != nil {
		t.Fatal("CLOWN_DISABLE_STATSD=1 must disable the client")
	}
	// All methods must be nil-safe no-ops.
	c.timing("x", time.Second)
	c.incr("x")
	c.emitOutcome("x", time.Now(), "success")
}

func TestStatsdUnknownServerNameDefault(t *testing.T) {
	next := statsdListener(t)
	t.Setenv(statsdDisableEnvVar, "")
	t.Setenv(serverNameEnvVar, "")
	c := newStatsdFromEnv()
	c.incr("m.success")
	if got := next(); got != "clown.bridge.unknown.m.success:1|c" {
		t.Errorf("default-server datagram = %q", got)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// statsd instrumentation for bridged MCP requests (stats-me / statsd line
// protocol over fire-and-forget UDP). Every bridged JSON-RPC request emits,
// under the prefix clown.bridge.<server>.<label>.:
//
//	.duration:<ms>|ms   on completed responses (success or failure)
//	.success:1|c        response delivered with no JSON-RPC error
//	.failure:1|c        JSON-RPC error response, bridge error, or queue-full
//	.abandoned:1|c      client context cancelled before a response arrived
//
// <server> comes from CLOWN_BRIDGE_SERVER_NAME (set by clown's stdioServers
// desugaring); <label> is the tool name for tools/call, else the JSON-RPC
// method, both statsd-sanitized. Emission must never block or fail the
// request path: the client is nil (a no-op) when disabled or when the UDP
// dial fails, and write errors are swallowed.
const (
	statsdHostEnvVar    = "STATSD_HOST"
	statsdPortEnvVar    = "STATSD_PORT"
	statsdDisableEnvVar = "CLOWN_DISABLE_STATSD"
	serverNameEnvVar    = "CLOWN_BRIDGE_SERVER_NAME"

	statsdDefaultHost = "127.0.0.1"
	statsdDefaultPort = "8125"
)

// statsdClient emits statsd metrics over a connected UDP socket. A nil
// client is valid and makes every method a no-op.
type statsdClient struct {
	conn   net.Conn
	prefix string
}

// newStatsdFromEnv builds the bridge's statsd client from the standard
// statsd env vars, on by default at 127.0.0.1:8125. Returns nil (disabled)
// when CLOWN_DISABLE_STATSD=1 or the UDP dial fails.
func newStatsdFromEnv() *statsdClient {
	if os.Getenv(statsdDisableEnvVar) == "1" {
		return nil
	}
	host := os.Getenv(statsdHostEnvVar)
	if host == "" {
		host = statsdDefaultHost
	}
	port := os.Getenv(statsdPortEnvVar)
	if port == "" {
		port = statsdDefaultPort
	}
	conn, err := net.Dial("udp", net.JoinHostPort(host, port))
	if err != nil {
		return nil
	}
	server := os.Getenv(serverNameEnvVar)
	if server == "" {
		server = "unknown"
	}
	return &statsdClient{
		conn:   conn,
		prefix: "clown.bridge." + sanitizeMetric(server) + ".",
	}
}

// timing emits a statsd timer (<prefix><name>:<ms>|ms). Nil-safe; errors
// are swallowed (fire-and-forget).
func (c *statsdClient) timing(name string, d time.Duration) {
	if c == nil {
		return
	}
	_, _ = fmt.Fprintf(c.conn, "%s%s:%d|ms", c.prefix, name, d.Milliseconds())
}

// incr emits a statsd counter increment (<prefix><name>:1|c). Nil-safe;
// errors are swallowed (fire-and-forget).
func (c *statsdClient) incr(name string) {
	if c == nil {
		return
	}
	_, _ = fmt.Fprintf(c.conn, "%s%s:1|c", c.prefix, name)
}

// sanitizeMetric maps a name onto the statsd-safe alphabet: [A-Za-z0-9_-]
// is kept, every other rune becomes '_' (so tools/call -> tools_call).
func sanitizeMetric(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// metricLabel returns the per-request metric label: the tool name for
// tools/call (the granularity that answers "which tool is slow/flaky"),
// else the JSON-RPC method. Always statsd-sanitized.
func metricLabel(method string, body []byte) string {
	if method == "tools/call" {
		var probe struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &probe) == nil && probe.Params.Name != "" {
			return sanitizeMetric(probe.Params.Name)
		}
	}
	return sanitizeMetric(method)
}

// responseOutcome classifies a delivered JSON-RPC response for emitOutcome.
func responseOutcome(resp json.RawMessage) string {
	if responseIsError(resp) {
		return "failure"
	}
	return "success"
}

// responseIsError reports whether a JSON-RPC response carries an error
// member (the success/failure discriminator for delivered responses).
func responseIsError(resp json.RawMessage) bool {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(resp, &probe) != nil {
		return false
	}
	return len(probe.Error) > 0 && string(probe.Error) != "null"
}

// emitOutcome records one bridged request's terminal outcome: counters for
// every outcome, plus duration for completed responses (success/failure —
// abandoned requests never completed, so their elapsed time would skew the
// timer).
func (c *statsdClient) emitOutcome(label string, started time.Time, outcome string) {
	if c == nil {
		return
	}
	switch outcome {
	case "success", "failure":
		c.timing(label+".duration", time.Since(started))
	}
	c.incr(label + "." + outcome)
}

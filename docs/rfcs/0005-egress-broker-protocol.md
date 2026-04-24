---
status: draft
date: 2026-04-24
---

# Egress Broker Protocol

## Abstract

This specification defines the interface between a sandboxed subagent and a per-invocation HTTP (Hypertext Transfer Protocol) proxy — called the egress broker — that mediates all network traffic leaving the sandbox. The broker is the enforcement point for per-subagent domain allowlists, rate limits, secret injection, and audit logging. This RFC specifies the protocol; the broker implementation is intentionally unspecified and pluggable.

The egress broker is distinct from any MCP servers the subagent may have available inside its chroot. Those MCP servers are regular tools reachable over stdio (no network required). The broker sits on the outbound-network boundary and only handles HTTP egress.

## Introduction

FDR-0001 specifies that sandboxed subagents have no direct network access. The subagent's chroot is constructed so that the only reachable network endpoint is a loopback HTTP proxy bound to an ephemeral port. The proxy enforces a declarative policy on behalf of clown and injects host-side secrets into upstream requests.

This RFC pins down three things: how the broker is launched and addressed, the policy inputs it receives, and the behavior it MUST exhibit on the proxy-facing side. Any executable that satisfies this contract and speaks the clown plugin protocol (RFC-0002) can serve as the broker.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be interpreted as described in RFC 2119.

## Specification

### 1. Lifecycle

For each sandboxed subagent invocation with `sandbox.egress.broker` set (and not `"none"`), ringmaster (RFC-0004) spawns a fresh broker process. The broker is terminated when the invocation completes, regardless of outcome. Brokers are never shared across invocations. See ADR-0003 for the rationale.

The broker executable path MUST be declared in the subagent's Nix closure — typically via the `[sandbox.egress.broker]` field resolving to a flake output. Clown provides no default broker; consumers pick one per subagent.

### 2. Launch Contract

Ringmaster launches the broker with:

```
<broker-binary> \
  --policy-file <policy-path> \
  --invocation-id <uuid> \
  --audit-log <log-path>
```

and stdin/stdout connected to a clown-protocol handshake (RFC-0002). The broker MUST emit a handshake line of the form:

```
1|1|tcp|127.0.0.1:<port>|http
```

within 5 seconds of startup. The `http` transport indicates this is a plain HTTP CONNECT/forward proxy, not an MCP (Model Context Protocol) server.

After the handshake, the broker MUST NOT close stdout or stdin until it receives a termination signal. Closing either is treated by ringmaster as a crash and fails the invocation.

### 3. Policy File

The policy file is JSON written by ringmaster before broker launch. Its schema:

```json
{
  "invocation_id": "<uuid>",
  "subagent_name": "<n>",
  "allowlist": [
    { "host": "api.anthropic.com", "ports": [443] }
  ],
  "rate_limit": { "requests_per_minute": 60 },
  "secrets": [
    {
      "match": { "host": "api.anthropic.com", "header": "x-api-key" },
      "inject_from": "env:ANTHROPIC_API_KEY"
    }
  ],
  "audit": {
    "log_path": "<log-path>",
    "include_request_bodies": false
  }
}
```

- `allowlist` is exhaustive. Any request to a host/port not listed MUST be rejected with HTTP 403 and MUST be logged.
- `secrets.match` specifies the host and the header name to be injected. `inject_from` is one of `env:<NAME>` (read from broker's env at launch — clown passes host-side secrets this way), `file:<path>` (read from a file readable by the broker), or `command:<shell-cmd>` (exec and capture stdout).
- `audit.include_request_bodies` defaults to `false`. Even when `true`, bodies larger than 16 KiB MUST be truncated in the log.

Policies are immutable for the lifetime of a broker instance; hot reload is not supported in v1.

### 4. Proxy Behavior

The broker MUST implement both:

- **HTTP forward proxy** (`GET http://...`, `POST http://...`) for plaintext upstreams.
- **HTTP CONNECT** tunneling for TLS (Transport Layer Security) upstreams.

On CONNECT for an allowlisted host, the broker MAY:

- Terminate TLS locally using a per-invocation CA (Certificate Authority) whose root is bind-mounted into the subagent's chroot as `/etc/ssl/broker-ca.pem`, allowing content-level inspection and secret injection. This is the RECOMMENDED mode.
- Tunnel bytes transparently without inspection, in which case secret injection is not possible for that upstream. Brokers SHOULD refuse secret-injection rules against upstreams they intend to tunnel.

When the broker terminates TLS locally, the per-invocation CA certificate MUST be generated at broker startup, written to a file, and never reused across invocations. The CA MUST NOT be installed in the host's trust store.

### 5. Secret Injection

For each request matching a `secrets.match` entry, the broker MUST:

1. Resolve the `inject_from` source at request time (not at broker startup — allows refreshing rotated credentials between requests).
2. Add or overwrite the specified header on the upstream request.
3. Strip the placeholder value from the subagent's original request before logging, so the audit log never contains the real secret or a subagent-supplied fake.

The subagent MUST NOT need to know the real secret. Clown's convention is to pass a per-invocation placeholder (e.g. `CLOWN_AUTH_TOKEN_<uuid>`) in the subagent's environment as the "API key," which the broker validates against the invocation ID before injecting the real key. This ensures a subagent whose process somehow leaks its environment leaks only the placeholder.

### 6. Rate Limiting

The broker MUST enforce `rate_limit.requests_per_minute` across all outbound requests from the invocation. The recommended algorithm is a token bucket refilled continuously. When the limit is exceeded, the broker MUST return HTTP 429 with `Retry-After` set.

Rate-limit rejections count as audit events.

### 7. Audit Log Format

The broker writes one JSON object per line (JSONL) to the declared audit log path. Each line MUST contain at minimum:

```json
{
  "ts": "2026-04-24T15:04:05.123Z",
  "invocation_id": "<uuid>",
  "method": "POST",
  "host": "api.anthropic.com",
  "port": 443,
  "path": "/v1/messages",
  "status": 200,
  "bytes_up": 4321,
  "bytes_down": 8192,
  "duration_ms": 742,
  "decision": "allow" | "deny_host" | "deny_rate"
}
```

Request and response bodies MAY be included per `audit.include_request_bodies` but MUST NOT include the injected secret.

### 8. Termination

Ringmaster terminates the broker after the subagent derivation completes by:

1. Closing the broker's stdin. A well-behaved broker MUST exit cleanly on stdin close.
2. If the broker has not exited within 5 seconds, sending SIGTERM.
3. If the broker has not exited within 10 seconds total, sending SIGKILL.

The broker MUST flush the audit log to disk before exiting in steps 1 and 2.

### 9. `broker = "none"`

When the subagent's config sets `broker = "none"`, no broker is launched and the subagent's bwrap config MUST include `--unshare-net`. The subagent has literally no network interfaces other than loopback with no listening services. This mode is REQUIRED for `backend = "circus"` (local llama-server) and RECOMMENDED for any subagent that does not need outbound network.

## Failure Modes

| Failure | Broker behavior | Ringmaster behavior |
| --- | --- | --- |
| Secret source unreachable at request time | Return 500 to subagent; log with `decision="deny_secret_unavailable"` | None; subagent sees request failure |
| Upstream connection fails | Return 502 to subagent; log upstream error | None |
| Broker itself crashes | stdout EOF | Fail invocation with `agent_error`, tear down sandbox |
| Audit log write fails | Exit with non-zero status, log to stderr | Treated as broker crash |

## Security Considerations

The broker runs on the host with access to host-side secrets. It is trusted infrastructure, not sandboxed. A compromise of the broker compromises every subagent invocation that uses it — which is why each invocation gets a fresh process, bounded to the lifetime of one derivation.

Per-invocation CAs are generated with ephemeral private keys held only in the broker process memory. Brokers SHOULD use a 1-hour validity on generated certificates; subagents that run longer than an hour will see cert expiry as an upstream failure, which is a feature.

The broker's policy file is the single point of truth for what the subagent can reach. Any tool claiming to bypass the broker (e.g. a subagent-side `curl` with `--no-proxy`) will fail on the netns level — the only route to any external IP is via the broker's socket.

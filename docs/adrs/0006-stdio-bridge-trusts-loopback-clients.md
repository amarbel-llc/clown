---
status: accepted
date: 2026-04-29
---

# ADR-0006: clown-stdio-bridge Trusts Loopback Clients (no HTTP hardening)

## Context

`clown-stdio-bridge` (FDR-0002) wraps a stdio MCP server and exposes it
over streamable-HTTP for `clown-plugin-host`. The bridge runs an
`http.Server` and historically set `ReadHeaderTimeout: 10 * time.Second`
to defend against Slowloris-style stalls.

The bridge's listener binds to `127.0.0.1:0` (loopback, ephemeral port).
The chosen port is communicated to the parent `clown-plugin-host` via a
handshake line on the bridge's stdout. There is no authentication on the
`/mcp` or `/healthz` endpoints — the bridge relies on the listener
address itself being unreachable from outside the host.

The question is whether HTTP-layer hardening (Slowloris timeouts in
particular) is load-bearing in this context.

## Options Considered

### Option A: Keep `ReadHeaderTimeout` (status quo)

Defends against Slowloris. Cheap, no behavioral cost — the 10-second
window is far longer than any legitimate header-read.

The realistic adversary for this defense is *an HTTP client that opens
many connections and sends headers a byte at a time over the network*.
For `clown-stdio-bridge`, no such client exists in the threat model:
the listener is loopback-only, the port is ephemeral and announced only
to the parent process via a private stdout handshake, and clown is a
developer tool — not an internet-facing service.

### Option B (this ADR): Drop HTTP hardening, trust loopback clients

Treat the listener as a private channel between two processes on the
same host. No `ReadHeaderTimeout`, no read/write/idle timeouts. The
bridge accepts whatever the parent sends and replies for as long as the
underlying MCP exchange takes (response bodies may be long-lived SSE
streams; request bodies may carry sizeable MCP payloads).

A local attacker who can reach the listener can already do worse things
than Slowloris — they're on the same machine, with whatever privileges
they got there.

## Decision

Adopt Option B. `clown-stdio-bridge` does not apply HTTP-level hardening
against misbehaving clients. The threat model is "trusted parent on the
same host"; defenses that only matter for arbitrary network traffic are
removed as dead weight.

This ADR is intentionally narrow: it covers `clown-stdio-bridge` and
similar future loopback-only bridges within clown. It does not authorize
removing hardening from anything that listens on a public interface.

## Consequences

### What we give up

- **Slowloris resilience.** A local process that learns the ephemeral
  port could open connections and send headers slowly, holding goroutines
  open. In the cooperative local threat model this is not a meaningful
  attack surface.
- **Defense in depth.** If the bridge is ever repurposed to listen on a
  non-loopback interface, the missing timeouts become a real
  vulnerability. Anyone making that change MUST revisit this ADR.

### What we gain

- **Less code, fewer magic numbers.** The 10-second value was a
  conventional pick, not a measured one. Removing it eliminates a
  parameter we'd otherwise have to justify or tune.
- **Aligned with reality.** The hardening signaled a threat model the
  rest of the bridge's design (no auth, loopback bind, port via private
  stdout) does not match. The new shape is internally consistent.

### Invariants this ADR depends on

- The bridge MUST continue to bind only to a loopback address.
- The handshake announcing the port MUST stay on a private channel
  (stdout to the parent), not a discoverable file or environment
  variable readable by other UIDs.
- If either invariant changes, this decision should be reopened.

## References

- FDR-0002 — stdio MCP bridge design.
- `cmd/clown-stdio-bridge/main.go` — listener binds to `127.0.0.1:0`;
  port announced via handshake on stdout.

---
status: accepted
date: 2026-04-29
---

# ADR-0006: clown-stdio-bridge Trusts Loopback Clients (no HTTP hardening)

> **Note (2026-10-24 audit), facts only; whether to reopen is a decision.**
> - The bridge also serves `/clown/system-prompt` and
>   `/clown/exclude-tools`, not only `/mcp` and `/healthz`.
> - It has a second listener mode: `--listen-unix --peer-unit` (FDR 0020
>   F12). There it admits only the agent unit and the parent clown, and
>   refuses the unit on the control routes (clown#301).
> - The loopback mode this ADR describes is unchanged, and clown#288 (open)
>   challenges its premise that any local process may connect.
> - Inside an agent unit, an in-unit bridge's loopback belongs to the
>   worker (FDR 0020).

> **Corrections (2026-11-12, clown#288; operator ruling: the threat model
> includes other local users and confined processes).**
> - The port is **not** private. It is announced to the parent on stdout, but
>   any local user can list the host's listening sockets, so "announced only
>   to the parent" and "a private channel" do not hold. Read "private stdout
>   handshake" below as "not published anywhere a client is meant to look".
> - "A local attacker who can reach the listener can already do worse things"
>   holds for a process running as the same user, which can read the
>   manifest and the bridge's environment anyway. It does not hold for another
>   user on the host, or for a confined process (an agent unit) that can reach
>   loopback but cannot read those files. Loopback reachability is not
>   authority, and the bridge no longer relies on it alone: with
>   `CLOWN_BRIDGE_TOKEN` set it requires `Authorization: Bearer <token>` on
>   every route but `/healthz` (off by default). **Follow-up (2026-11-12):** the
>   host now mints one random token per session when launched with
>   `CLOWN_BRIDGE_AUTH=1` (default off, claude provider only, not with
>   `--mcp-collapse`; confined launches are covered since 2026-11-12, see
>   below), gives it to every bridge it starts,
>   names it in claude's manifest `headers` (checked live on claude 2.1.318 and
>   2.1.322, `just explore-claude-mcp-headers`) and sends it on its own control
>   calls. It never logs the token. A process that cannot read the manifest or
>   the bridge's environment can no longer use the port; a same-user process
>   still can, as before. The design is in
>   `docs/plans/2026-11-11-clown-241-288-design.md`.
> - **Confined launches (`--agent-scope`, 2026-11-12).** The relays into the
>   unit are byte splices, so claude's `Authorization` header passes through
>   them to the frontend bridges (ringmaster, troupe, the diagnostics server,
>   spinclass's serve socket) unchanged. The agent-side bridges the in-unit relay
>   starts get the token in their env (the servers file entry is the same env
>   the frontend built), and the manifest entries for them carry the header; the
>   relay's own tool-exclusion push and catalog fetch send it too. What a process
>   in the unit can read: the manifest, the servers file (0600, the unit's own
>   user) and an in-unit bridge's `/proc/<pid>/environ`, so the token is not
>   secret from the agent, as with the manifest outside a unit. The frontend
>   bridges' environments are outside the unit's pid namespace. For a confined
>   launch the token therefore protects the frontend loopback bridges against
>   other local users and against processes outside the unit; it adds nothing
>   against the agent itself, and the peer-credential check on the serve socket
>   stays the gate for that socket.
> - The Slowloris decision itself (no HTTP read/write timeouts) is unchanged.
>   The unix-socket mode with peer credentials is a separate, open discussion.

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

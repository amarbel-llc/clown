---
status: draft
date: 2026-04-26
---

# Stdio MCP Servers via Host-Side HTTP Bridge

## Abstract

Clown today supports two paths for plugin-supplied MCP servers: HTTP servers
declared in `clown.json` and managed by `clown-plugin-host` (RFC 0002), and
stdio servers declared in `.claude-plugin/plugin.json`'s `mcpServers` block
and spawned directly by Claude Code. The latter inherits Claude Code's
process tree, environment, and any future host-level sandbox around Claude.
This record specifies a third path: stdio MCP servers declared in
`clown.json` under a new `stdioServers` block, transparently bridged to
streamable-HTTP by a clown-supplied bridge binary that runs outside any
Claude sandbox under `clown-plugin-host`'s ordinary lifecycle.

The bridge wraps a stdio MCP server, exposes it on a loopback HTTP port,
and speaks the clown plugin protocol handshake (RFC 0002) on its own
stdout. From Claude Code's perspective the result is indistinguishable
from a native HTTP MCP server. From the plugin author's perspective, the
bridge is an implementation detail: they declare a stdio MCP and clown
desugars it to `httpServers` + bridge internally.

The design is motivated by issue #28 and rests on the trust-boundary
posture in RFC 0006 (clown imposes no sandbox on plugins; plugins own
their isolation; this FDR adds no new isolation, only a transport).

## Motivation

Stdio MCP servers commonly need host resources that a future
process-level sandbox around Claude Code would deny: Unix sockets for
agents (ssh-agent, gpg-agent, pivy-agent), the user's keychain, the
real `$HOME`, network namespaces, hardware devices, browser profiles,
and arbitrary system tools on `$PATH`. Today these servers happen to
work because Claude Code is unsandboxed, so child processes inherit
ambient privileges. That is not a property to rely on.

Three other gaps in the current setup motivate this work even before
any future Claude sandbox lands:

1. **Discovery uniformity.** HTTP MCPs are discovered from
   `clown.json`; stdio MCPs are discovered from
   `.claude-plugin/plugin.json`. Plugin authors deciding between
   transports have to learn two manifest shapes. A single `clown.json`
   declaration for both transports — with the choice of HTTP vs stdio
   becoming an implementation detail of the plugin — collapses that
   decision tree.

2. **Lifecycle uniformity.** HTTP servers under `clown-plugin-host`
   get a structured handshake, healthcheck, log routing, and graceful
   shutdown. Stdio servers spawned by Claude Code get whatever
   lifecycle Claude provides. Routing stdio MCPs through the same
   `clown-plugin-host` lifecycle gives them log routing and clean
   teardown for free.

3. **Compilation parity.** RFC 0002 already compiles per-plugin
   `plugin.json` to inject HTTP-based `mcpServers` entries pointing at
   running servers. With stdio servers also routed through the bridge,
   the compiled `plugin.json` becomes uniform (every entry is an
   `http`/`sse` URL), and Claude Code never spawns a server itself.

## Non-Goals

This design does **not** add isolation. The bridge is a transport
adapter, not a sandbox. The wrapped stdio server runs with the
bridge's environment, which is the user's environment. Any isolation
the plugin author wants must be applied at or below the wrapped
command, per RFC 0006.

This design does **not** support arbitrary fd inheritance, named-pipe,
or socket-fd MCP transports. The wrapped command is required to speak
JSON-RPC 2.0 over its own stdin/stdout — the standard MCP stdio
shape.

This design does **not** modify Claude Code or the MCP specification.
The bridge appears to Claude Code as a normal streamable-HTTP MCP
server.

## Design Overview

The design has three pieces:

### 1. Schema addition

`clown.json` gains an optional `stdioServers` map alongside
`httpServers`. Each entry mirrors the stdio MCP shape:

```json
{
  "version": 1,
  "stdioServers": {
    "kagi": {
      "command": "kagi-mcp",
      "args": ["--api-key-env", "KAGI_KEY"],
      "env": { "LOG_LEVEL": "info" }
    }
  }
}
```

Fields: `command` (required), `args` (optional), `env` (optional),
`timeout` (optional, integer ms; semantics match RFC 0002 §1.2). No
`transport` field — always stdio. No `healthcheck` — the bridge
synthesizes startup readiness on the wrapped child's behalf.

### 2. Internal desugaring

At parse time, each `stdioServers.<name>` entry is transformed into a
synthesized `httpServers.<name>` entry of the form:

```json
{
  "command": "<path-to-clown-stdio-bridge>",
  "args": ["--command", "<original-command>", "--", "<...original-args>"],
  "env": { "<original-env>" },
  "transport": "streamable-http",
  "timeout": "<original-timeout>",
  "healthcheck": {
    "path": "/healthz",
    "interval": "1s",
    "timeout": "30s"
  }
}
```

The wrapped command is given to the bridge via `--command`, and
everything after `--` becomes the wrapped command's argv. This shape
is unambiguous even when the wrapped command's own args contain `--`,
which the plain-`--`-separator alternative could not handle. Plugin
authors never see this argv — they write the high-level
`stdioServers` form and clown desugars it. The explicit shape exists
for the desugaring code, for `ps` readability, and for ad-hoc
debugging by maintainers running the bridge by hand.

The synthesized entry is indistinguishable from a hand-written
`httpServers` entry to the rest of `clown-plugin-host`. Discovery,
launch, handshake, healthcheck, log routing, manifest compilation,
and shutdown all reuse the existing code paths.

`stdioServers` and `httpServers` MAY both be present in the same
manifest. Names MUST be unique across both maps.

### 3. The bridge binary

Clown ships a new binary, `clown-stdio-bridge`, alongside
`clown-plugin-host`. When launched by `clown-plugin-host` it:

1. Parses its own args. The wrapped command is given via
   `--command <cmd>`. Everything after `--` is taken verbatim as
   the wrapped command's argv.
2. Spawns the wrapped command as a child, with stdin/stdout
   pipes. The child's stderr is forwarded to the bridge's stderr
   (so the existing `forwardStderr` path in `clown-plugin-host`
   picks it up under the bridge's prefix).
3. Binds an ephemeral loopback TCP port.
4. Prints the clown plugin protocol handshake on its own stdout
   (`1|1|tcp|<addr>|streamable-http\n`) and flushes.
5. Serves streamable-HTTP MCP on that port. Each incoming MCP
   message is serialized to JSON-RPC 2.0 and written as a single
   line to the wrapped child's stdin. Each line read from the
   child's stdout is parsed as JSON-RPC 2.0 and routed back to
   the appropriate HTTP request or SSE notification stream.
6. Responds to `GET /healthz` with HTTP 200 once the child has
   produced its first stdout line (or after a small bounded
   delay, whichever comes first).
7. On `SIGTERM`, sends `SIGTERM` to the wrapped child, drains
   pending responses for up to a small grace period, and exits.

The bridge does not interpret MCP semantics. It is a JSON-RPC pipe
with framing translation. The bridge supports `streamable-http`
only; `sse` is not in v1 and would be a non-breaking addition if a
future Claude Code version dropped streamable-http support.

#### Crash behavior

If the wrapped child exits unexpectedly, the bridge logs and exits
non-zero. `clown-plugin-host`'s existing failure path
(`StartReport.Failed`, `--skip-failed`, the unhealthy-server flow)
handles restart-or-abort. The bridge stays a transport adapter,
not a supervisor. A future v0.4.0 enhancement may add an optional
`restart: never|on-failure|always` field to `stdioServers`
entries; the v1 default is "never" (i.e., the current behavior).

#### Resource limits

The bridge bounds memory by capping the number of pending messages
in each direction. On overflow:

- **Inbound (HTTP → child stdin)**: the offending HTTP request
  is rejected with an MCP-level error. The wrapped child stays
  alive. The operator sees the error in logs and on the failing
  request.
- **Outbound (child stdout → HTTP/SSE)**: the bridge logs a
  warning and drops the oldest pending message to make room. A
  metric or counter exposes the drop count for diagnosis.

Exact limits are tuning parameters for the implementing RFC.

## Trust Boundary

The bridge runs in the same trust tier as any other
`clown-plugin-host`-managed server: outside any present or future
Claude sandbox, with the user's environment and privileges. The
wrapped stdio child inherits that tier.

This is consistent with RFC 0006: clown imposes no sandbox on
plugins; the plugin author owns isolation. The bridge does not
weaken the trust boundary — a plugin author who declares a stdio
server already accepts that the server runs unsandboxed. The
bridge merely makes that runtime location uniform with HTTP
servers (outside the eventual Claude sandbox) and predictable
under `clown-plugin-host`'s lifecycle.

The loopback port is ephemeral and bound to `127.0.0.1` only,
matching RFC 0002's posture for HTTP servers. Local-user attackers
on the same host can reach it; that is in the same threat tier as
reaching any other clown-plugin-host-managed server.

## Compilation Behavior

For every plugin that declared at least one `stdioServers` entry,
`clown-plugin-host` compiles `plugin.json` per RFC 0002 §3.6 — the
synthesized `httpServers` entries flow through the existing
compilation path unchanged. The compiled manifest's
`mcpServers.<name>` entry is a `{type: http, url: ...}` pointing
at the bridge's loopback port; the original stdio command never
appears in what Claude Code sees.

If the source `.claude-plugin/plugin.json` already declared an
`mcpServers.<name>` stdio entry with the same name, the
clown-plugin-host-injected entry replaces it, and the original
stdio command is no longer spawned by Claude Code. This is the
intended migration path: plugin authors move declarations from
`plugin.json`'s `mcpServers` block into `clown.json`'s
`stdioServers` block, and the wrapped server now runs outside the
Claude tree.

## Concurrency

Streamable-HTTP MCP allows multiple concurrent client sessions
against one server. A stdio MCP server has only one stdin/stdout
pair. The bridge serializes outgoing requests to the child:
multiple concurrent HTTP requests are queued and dispatched in
arrival order; correlation back to HTTP responses uses the
JSON-RPC `id` field, which is preserved end-to-end.

For v1, server-initiated notifications (`tools/list_changed`,
etc.) are broadcast to every connected SSE stream. If a future
need arises to scope notifications per session, that can be added
later without breaking the wire shape.

If the wrapped child does not preserve `id` correlation
faithfully (a buggy server), the bridge cannot recover. This is a
plugin-author bug; the bridge SHOULD log and surface it via its
own stderr for diagnosis.

## Open Questions for the Implementing RFC

A follow-up RFC (number to be assigned) will pin down:

- Whether `stdioServers` entries should support a `cwd` field.
  Deferred from v1; add when a real plugin needs it.
- Exact buffer-limit values for the resource-limit caps described
  in §3.
- Wire details of streamable-HTTP MCP that the bridge must
  faithfully implement (session IDs, SSE keepalive cadence,
  error-response shapes).

## Future Work

Items intentionally out of v1 but already scoped:

- **v0.4.0 — `restart` field on `stdioServers`.** Optional
  `restart: never|on-failure|always` so plugin authors can opt
  into bridge-level supervision without going through
  `clown-plugin-host`'s failure path. Default would remain
  `never` to preserve current behavior.
- **Later — `sse` transport.** Only if a Claude Code version
  ever drops streamable-http; until then there is no caller to
  serve.

## Alternatives Considered

### A. Spawn-a-shim-inside-the-sandbox

The shim runs as a child of Claude Code (inside the future Claude
sandbox), proxying stdin/stdout to a Unix socket where the real
stdio MCP is listening (outside the sandbox).

Rejected. The shim-inside-sandbox model requires the sandbox to
permit at least Unix-socket connections to a known path, which is
both more permission than necessary and harder to express in a
generic sandbox policy than "no syscalls except loopback TCP."
The HTTP bridge approach reuses the loopback-TCP capability that
RFC 0002 already requires for HTTP servers, so the future
sandbox's network policy collapses to one rule.

### B. Pass through `mcpServers` stdio entries unchanged

Keep stdio servers in `.claude-plugin/plugin.json`'s `mcpServers`
block. Document that plugin authors who care about future
sandboxing should rewrite their server in HTTP.

Rejected. Putting the burden on every stdio MCP author to either
reimplement transport plumbing or live with the inheritance
problem is a regression from "clown handles your transport." It
also forecloses the discovery- and lifecycle-uniformity gains.

### C. Schema option: `httpServers.<name>.command = clown-stdio-bridge`

Document the bridge as a binary plugin authors invoke explicitly
from their `httpServers` entry, rather than introducing a separate
`stdioServers` schema block.

Rejected (per discussion on #28). The explicit-bridge form leaks
implementation into the manifest. A new high-level
`stdioServers` block is friendlier for authors, lets clown swap
the bridge implementation later without a schema change, and
makes the desugaring location obvious to readers of clown.json.

## References

- Issue #28 — Support clown-plugin protocol for stdio MCPs to
  bootstrap outside sandbox
- RFC 0002 — Clown Plugin Protocol: HTTP MCP Server Lifecycle
  Management
- RFC 0006 — Clown Plugin Sandboxing Posture (draft)
- MCP specification: stdio transport
  <https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#stdio>
- MCP specification: streamable-HTTP transport
  <https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#streamable-http>

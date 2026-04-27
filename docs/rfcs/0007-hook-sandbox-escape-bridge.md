---
status: draft
date: 2026-04-27
---

# Hook Sandbox-Escape Bridge

## Abstract

This specification defines a host-mediated bridge that lets provider hooks
perform host-level work even when the provider runs inside a sandbox. The
host (clown) prepares a unix-domain socket, registers a dispatch table
mapping `(plugin, event, ordinal)` triples to handler commands, and
configures the provider's hooks pristinely via the provider's
`--settings` flag rather than by mutating settings files. Each
configured hook entry resolves to a small clown-supplied shim
(`clown-hook-shim`) that the sandboxed provider spawns; the shim
proxies the hook's stdin, stdout, stderr, and exit code over the
socket, and the host runs the registered handler in the host's
working directory and environment.

The bridge is a transport adapter. It does not impose a sandbox, does
not assume which sandbox is in use, and does not add isolation. It is
designed to function under macOS `sandbox-exec`, Linux `bwrap`
(bubblewrap), Linux Landlock, or any combination thereof, by relying
only on a single filesystem path being grantable inside the sandbox.

## Introduction

Provider hooks (`PreToolUse`, `PostToolUse`, `Stop`, and others
described in `claude-code-hooks(5)`) are spawned as child processes
of the provider. When the provider runs in a sandbox scoped to a
session worktree or other narrow root, hooks inherit that scope and
lose access to host resources they may legitimately need: cross-session
state files, per-user configuration, sibling-directory `git`
invocations, runtime sockets for agents, and so on. Disabling the
sandbox to make hooks work defeats the sandbox's purpose. The same
problem motivates FDR 0002 for stdio MCP servers; this RFC addresses
the analogous problem for hooks.

The mechanism proposed here is host-mediated rather than peer-to-peer.
Clown owns the socket, the dispatch table, and the lifecycle. The
sandboxed provider can only invoke handlers the host has pre-registered
from `clown.json` manifests; it cannot ask the host to run arbitrary
commands. This keeps the escape explicit and auditable, as required by
issue #34.

This RFC does not impose a sandbox of its own. RFC 0006 establishes
that clown takes no position on plugin isolation; the same posture
applies here. The bridge is for the case where some *other* component
(`clownbox`, a future provider-side sandbox, the user's own
configuration) supplies the sandbox.

This RFC builds on:

- **RFC 0002** (clown plugin protocol) for the manifest schema and
  plugin discovery pipeline.
- **RFC 0006** (sandboxing posture) for the trust posture inherited
  here.
- **FDR 0002** (stdio MCP bridge) for the precedent of a clown-supplied
  helper that bridges in-sandbox processes to out-of-sandbox state.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
"SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this
document are to be interpreted as described in RFC 2119.

## Specification

### 1. clown.json schema extension

A `clown.json` manifest MAY include a top-level `hooks` field
alongside `httpServers` and `stdioServers`. The `hooks` field is an
object mapping provider hook event names (e.g. `"PreToolUse"`,
`"PostToolUse"`, `"Stop"`) to arrays of handler entries. Each handler
entry is an object with the following fields:

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `command` | string | Yes | — | Path to the handler binary on the host. Relative paths are resolved against the plugin directory. |
| `args` | array of strings | No | `[]` | Command-line arguments. |
| `env` | object | No | `{}` | Additional environment variables (key-value strings) merged onto the host environment when the handler runs. |
| `matcher` | string | No | `""` | Optional matcher passed through to the provider's settings, with the same semantics as `claude-code-hooks(5)`. |

The presence of an entry under `hooks` is the opt-in for sandbox
escape. There is no separate `bridge: true` flag: a plugin that does
not need host-level execution simply does not declare hooks under
`clown.json` (it can still declare hooks the conventional way via
`.claude/settings*.json`, which clown does not intermediate).

Older clown versions encountering the `hooks` field MUST ignore it,
consistent with RFC 0002 §1.2 (additive schema evolution).

### 2. Dispatch table

The host MUST construct a dispatch table by walking discovered
`clown.json` manifests and assigning each handler entry an opaque,
deterministic key of the form `(plugin-id, event-name, ordinal)`,
where:

- `plugin-id` is the plugin name as resolved per RFC 0002 §1.3.
- `event-name` is the provider hook event name as it appears in
  `clown.json`.
- `ordinal` is the zero-based index of the entry within its event's
  array, allowing a single plugin to register multiple handlers for
  the same event.

The dispatch table is the capability boundary: a sandboxed process
MAY only invoke entries that the host has placed in this table.
Entries not present in the table MUST be rejected at dispatch time
(see §6).

### 3. Socket location

The host MUST create one unix-domain socket per provider session at
the following path:

- On Linux: `$XDG_RUNTIME_DIR/clown/<session-id>/hooks.sock`. If
  `XDG_RUNTIME_DIR` is unset, the host MUST fall back to
  `$TMPDIR/clown-<uid>/<session-id>/hooks.sock`.
- On macOS: `$TMPDIR/clown-<session-id>/hooks.sock`.

`<session-id>` MUST be an unguessable opaque token, generated fresh
per session (e.g. 128 bits of CSPRNG output, hex-encoded).

The parent directory MUST be created with mode `0700`. The socket
itself MUST end up with mode `0600` after `bind()`. The host MUST
remove both the socket and its parent directory on clean shutdown.

On startup, if the target socket path already exists, the host MUST
attempt a `connect()`; if the connection is refused (`ECONNREFUSED`),
the host MUST `unlink()` the stale path before binding. If the
connection succeeds, the host MUST abort with an error: another
session is using that path.

### 4. Provider configuration

The host MUST configure the provider's hooks via the provider's
`--settings` flag, not by mutating `.claude/settings.local.json` or
any other on-disk settings file. Each entry in the dispatch table
contributes one entry to the provider's settings under
`hooks.<event-name>`, with the following shape:

```json
{
  "type": "command",
  "command": "<clown-hook-shim-path> <plugin-id> <event-name> <ordinal>",
  "matcher": "<matcher from clown.json, if any>"
}
```

The host MUST set the environment variable `CLOWN_HOOK_SOCK` to the
absolute socket path in the provider's environment.

If the user has supplied their own `--settings` argument, the host
MUST merge: the host's `hooks` block is union'd with the user's
`hooks` block per event. Implementations MAY choose either ordering
(host-first or user-first) but MUST document the choice and apply it
consistently.

The provider sees ordinary `command`-type hook entries; it requires
no awareness of the bridge.

### 5. Wire format

The shim and the host exchange a single request/response pair per
hook invocation, framed as length-prefixed segments. All lengths are
unsigned 32-bit big-endian integers ("uint32be").

#### 5.1 Request

The shim sends:

1. `uint32be` length of the JSON header that follows.
2. JSON header bytes. The header is a UTF-8 JSON object with these
   fields:
   - `plugin` (string, required): the plugin id.
   - `event` (string, required): the hook event name.
   - `ordinal` (integer, required): the dispatch-table ordinal.
   - `cwd` (string, required): the shim's current working directory
     at invocation time. Informational; the host does NOT use this
     as the handler's cwd (see §6).
   - `env` (object, optional): additional environment passed by the
     provider into the shim. Implementations MAY restrict this to
     a known prefix (e.g. `CLAUDE_*`) to avoid leaking ambient state
     into the handler.
3. `uint32be` length of the stdin bytes that follow.
4. The shim's stdin bytes verbatim (the provider's hook payload).

The shim MUST then half-close the write side of its socket
connection (`shutdown(SHUT_WR)`), signalling end-of-request.

#### 5.2 Response

The host replies:

1. `uint32be` length of the JSON header that follows.
2. JSON header bytes. The header is a UTF-8 JSON object with these
   fields:
   - `exit_code` (integer, required): the handler's exit code, in
     the range [0, 255].
3. `uint32be` length of the stdout bytes that follow.
4. The handler's stdout bytes verbatim.
5. `uint32be` length of the stderr bytes that follow.
6. The handler's stderr bytes verbatim.

The host MUST then close the connection.

#### 5.3 Frame limits

Implementations MUST accept frames at least 1 MiB long for stdin,
stdout, and stderr. They MAY accept larger frames. They MUST cap the
JSON header at 64 KiB and reject larger headers as a protocol
violation.

#### 5.4 Errors

If any frame is malformed (length-prefix overruns, JSON parse
failure, unknown required field missing), the receiving side MUST
close the connection without responding. It MUST NOT send a partial
response. The shim, on a closed-without-response connection, MUST
exit with code 127 and write `"clown: hook bridge protocol error\n"`
to its own stderr. The provider sees this as the hook handler
failing.

### 6. Host dispatch semantics

On accepting a connection, the host MUST verify peer credentials
match the user that started clown. On Linux this is done via
`SO_PEERCRED`; on macOS via `LOCAL_PEERCRED` or `getpeereid()`. A
mismatch MUST result in immediate connection close with no response.

After successful peer-cred check, the host reads the request frame
(§5.1), looks up `(plugin, event, ordinal)` in the dispatch table,
and:

- If absent: replies with `exit_code: 127`, empty stdout, and
  stderr `"clown: handler not registered for (<plugin>, <event>, <ordinal>)\n"`.
- If present: spawns the registered command with:
  - `cwd` set to the host's working directory at the time clown
    started the provider session — NOT the shim's reported cwd
    (matches the user's session worktree expectation).
  - `env` constructed by merging the manifest's declared `env`,
    then the request's `env`, onto the host's own environment in
    that order.
  - `stdin` connected to the request's stdin bytes.
  - `stdout` and `stderr` captured into in-memory buffers.

When the handler exits, the host MUST send the response frame (§5.2)
and close the connection.

### 7. Concurrency

The host MUST handle concurrent connections. Hooks may fire in
parallel for parallel tool calls or unrelated events. Each
connection runs its handler independently; there is no global
serialization. Implementations MAY apply a per-connection or
per-handler concurrency cap; if so, they MUST document it.

### 8. Lifecycle

The host MUST open the socket and have the dispatch loop accepting
connections before spawning the provider. The host MUST keep the
socket open for the lifetime of the provider process and MUST close
the listener and `unlink` the socket and parent directory after the
provider exits, regardless of provider exit code.

If the host is killed uncleanly (SIGKILL), the socket and directory
will remain on disk. The next session detects and unlinks stale
paths per §3.

### 9. Errors and timeouts

The host MUST NOT impose a timeout on handler execution. Provider
implementations already enforce hook timeouts (cf.
`claude-code-hooks(5)`), and double-bounding adds no value.

If the handler fails to start (e.g. binary missing,
`exec` failure), the host MUST treat that as the handler exiting with
code 127 and stderr describing the cause. Other low-level errors
(socket I/O failure mid-response, out-of-memory in the host) MUST
result in the host closing the connection without a response, which
the shim handles per §5.4.

## Security considerations

The peer-credential check is the trust gate. Unlike the loopback-TCP
bridge in FDR 0002, where origin checks at the HTTP layer defend
against same-host cross-process spoofing, the unix socket variant
relies on kernel-enforced peer-cred semantics. A different user on
the same host cannot connect: they cannot traverse the
`0700`-protected parent directory.

The dispatch table is the capability boundary. A sandboxed process
that obtains the socket path can only invoke commands the host has
pre-registered from `clown.json` manifests. It cannot ask the host
to run arbitrary shell, cannot supply its own argv beyond the
`(plugin, event, ordinal)` triple, and cannot escape the registered
command's argv shape.

Environment passthrough from the request's `env` field is intentional
but bounded. Plugins declared in `clown.json` already trust their own
plugin author; passing a few `CLAUDE_*` variables through is
consistent with that trust. Implementations SHOULD restrict request
`env` to a known prefix or allowlist rather than passing arbitrary
keys.

The socket path itself is not a secret per se — locally privileged
attackers can read process environments — but the `0700` parent
directory and peer-cred check make path leakage non-fatal.

## Compatibility

Plugins that do not declare a `hooks` block in `clown.json` are
unaffected. Existing `.claude/settings*.json` hook entries continue
to work unchanged; clown does not intercept them.

Older clown binaries reading a manifest with the new `hooks` field
MUST ignore it, per RFC 0002 §1.2.

The bridge does not modify the provider or the
`claude-code-hooks(5)` payload schema. From the provider's
perspective, a bridged hook is an ordinary `command`-type entry
whose binary happens to be `clown-hook-shim`.

## Open questions

1. **stderr surfacing.** The provider's hook protocol uses stdout for
   the structured decision payload and treats stderr as advisory log
   output. The current design returns both to the shim and lets it
   forward each to the matching fd. This is consistent with running
   the handler directly. If a provider configuration uses stderr for
   semantic signalling we have not seen, this may need revisiting.
2. **Streaming stdin.** Tool inputs for `Bash` or `Edit` could
   theoretically be very large. The current frame format buffers
   stdin entirely. A streaming variant (chunked frames with a
   continuation bit) is straightforward to add but adds complexity;
   defer until 1 MiB proves insufficient in practice.
3. **Per-event opt-in beyond registration.** The current design treats
   "registering a handler" as the opt-in. An alternative would be a
   separate `hookBridge: ["PreToolUse", ...]` array allowing a
   plugin to declare "these events MAY use the bridge" without
   registering specific handlers. We rejected this as redundant for
   the spinclass case; revisit if a plugin emerges that needs the
   distinction.
4. **Manifest field overlap.** `clown.json` already has `httpServers`
   and `stdioServers` as MCP-server containers; introducing `hooks`
   at the same level means clown.json now describes two
   conceptually different object kinds. Splitting into nested
   namespaces (`mcp.httpServers`, `hooks.<event>`) is cleaner but
   breaks compatibility with existing `clown.json` files. Defer.

## See also

- `claude-code-hooks(5)` — provider hook event schema and handler
  contract.
- `claude-code(1)` § Settings Options — `--settings <json>` flag.
- RFC 0002 — clown plugin protocol; manifest discovery pipeline.
- RFC 0006 — clown plugin sandboxing posture; trust posture
  inherited here.
- FDR 0002 — stdio MCP bridge; precedent for a host-side transport
  adapter.
- FDR 0004 — design of the clown-side helper and shim that
  implement this RFC.
- Issue #34 — the originating request.

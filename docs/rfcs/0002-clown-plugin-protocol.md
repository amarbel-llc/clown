---
status: testing
date: 2026-04-22
---

# Clown Plugin Protocol: HTTP MCP Server Lifecycle Management

## Abstract

This specification defines a protocol by which clown plugins can declare
HTTP-based MCP servers as local commands that are automatically launched at
session start and torn down at session end. The protocol covers server
discovery (via `clown.json` manifests), process lifecycle, handshake
negotiation (modeled on hashicorp/go-plugin), health checking, and MCP
configuration generation.

## Introduction

Claude Code's HTTP-based MCP transports (`streamable-http`, `sse`) enable
features unavailable over stdio:

- `notifications/tools/list_changed` — the server can push tool list updates
  to the client without the client polling
- Server-initiated requests via the persistent SSE announcement channel

Today, clown plugins can declare stdio MCP servers in `.mcp.json`, but HTTP
MCP servers must already be running at a known URL before the session starts.
This creates a gap: plugins that want to use HTTP transport features must
rely on external process management.

This specification introduces:

1. A `clown.json` manifest that plugins ship alongside `.claude-plugin/` to
   declare HTTP MCP servers
2. A `clown-plugin-host` binary that discovers, launches, health-checks, and
   cleans up these servers around the Claude Code session
3. A handshake protocol (adapted from hashicorp/go-plugin) for port
   negotiation between the host and child servers

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. clown.json Manifest

#### 1.1 Location

A `clown.json` file MUST be located at the root of a plugin directory, as a
sibling to the `.claude-plugin/` directory. The plugin directory MUST also
contain a valid `.claude-plugin/plugin.json` manifest with a non-empty `name`
field.

#### 1.2 Schema

The manifest is a JSON file with the following top-level fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `version` | integer | Yes | Schema version. MUST be `1`. |
| `httpServers` | object | Yes | Map of server name to server definition |

Each server definition is an object with the following fields:

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `command` | string | Yes | — | Path to the server binary. Relative paths are resolved against the plugin directory. |
| `args` | array of strings | No | `[]` | Command-line arguments |
| `env` | object | No | `{}` | Additional environment variables (key-value string pairs) |
| `transport` | string | No | `"streamable-http"` | MCP transport type: `streamable-http` or `sse` |
| `healthcheck` | object | No | See below | Health check configuration |

Healthcheck definition:

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `path` | string | No | `"/healthz"` | HTTP path to poll |
| `interval` | string | No | `"1s"` | Polling interval (Go `time.ParseDuration` format) |
| `timeout` | string | No | `"30s"` | Maximum time to wait for healthy |

#### 1.3 Example

```json
{
  "version": 1,
  "httpServers": {
    "my-server": {
      "command": "bin/my-server",
      "args": ["--mode", "mcp"],
      "env": { "LOG_LEVEL": "info" },
      "transport": "streamable-http",
      "healthcheck": {
        "path": "/healthz",
        "interval": "1s",
        "timeout": "30s"
      }
    }
  }
}
```

### 2. Handshake Protocol

The handshake protocol is modeled on hashicorp/go-plugin. Each HTTP MCP
server MUST print a single pipe-delimited line to stdout before serving
requests.

#### 2.1 Format

```
CORE_VER|APP_VER|NET_TYPE|NET_ADDR|PROTOCOL\n
```

| Field | Position | Type | Required | Values |
|-------|----------|------|----------|--------|
| Core protocol version | 0 | integer | Yes | MUST be `1` |
| App protocol version | 1 | integer | Yes | Integer (reserved, MUST be `1`) |
| Network type | 2 | string | Yes | MUST be `tcp` |
| Network address | 3 | string | Yes | `<host>:<port>` |
| Protocol | 4 | string | Yes | `streamable-http` or `sse` |

#### 2.2 Behavior

1. The server MUST bind to `127.0.0.1:0` (ephemeral port)
2. The server MUST print the handshake line to stdout
3. The server MUST flush stdout after printing the handshake
4. The server MUST begin serving HTTP requests after the handshake

Extra fields beyond position 4 MAY be present and MUST be ignored by the
host. The line MUST be terminated by `\n`. Leading and trailing whitespace
MUST be tolerated by the parser.

#### 2.3 Protocol Authority

The protocol field in the handshake line is authoritative. If it differs from
the `transport` field in `clown.json`, the handshake value takes precedence.
This allows servers to negotiate transport at runtime.

### 3. Server Lifecycle

#### 3.1 Discovery

`clown-plugin-host` receives plugin directories via `--plugin-dir` flags.
For each directory, it:

1. Checks for `clown.json` — if absent, skips the directory
2. Parses `clown.json` and validates the version field
3. Reads the plugin name from `.claude-plugin/plugin.json`
4. Records a `DiscoveredServer` for each entry in `httpServers`

If no `clown.json` files are found in any plugin directory,
`clown-plugin-host` MUST exec directly into the downstream command with
zero overhead (no child process, no intermediate config file).

#### 3.2 Launch

For each discovered server:

1. The command path is resolved relative to the plugin directory
2. The process is started in its own process group (`setpgid`)
3. The server's stderr is forwarded to the host's stderr with a
   `[plugin-name/server-name]` prefix
4. The first line of stdout is read as the handshake (section 2)
5. The health check endpoint is polled (section 3.3)

All servers are launched in parallel. The host collects per-server outcomes
into a start report and then decides whether to proceed, prompt, or abort;
see section 3.4.

#### 3.3 Health Checking

After reading the handshake, the host polls `http://<addr><healthcheck.path>`
at the configured interval. The server is considered healthy when it returns
HTTP 200. If the configured timeout elapses before a 200 response, the server
is killed and recorded as a failure.

#### 3.4 Failure Handling

A server is considered *failed* if any of the following occur: the child
process cannot be exec'd or exits early, its first stdout line is not a
valid handshake, its handshake does not arrive within the handshake timeout,
or its health check does not return HTTP 200 within the configured timeout.

The host emits one failure record per affected server (to stderr and to the
log file described in section 3.5), then chooses one of three branches:

| Context | Behavior |
|---------|----------|
| `--skip-failed` flag OR `CLOWN_SKIP_FAILED_PLUGINS=1` env var | Continue with only the healthy servers. If none came up, exec the downstream command directly (as in the no-`clown.json` case). |
| Interactive terminal (both stdin and stderr are TTYs) and no opt-in | Render a confirmation prompt listing every failure. Continuing is equivalent to `--skip-failed`; declining shuts down the healthy subset and exits non-zero. |
| Non-interactive, no opt-in | Shut down any healthy servers and exit non-zero. This is the default and preserves the pre-2026-04 behavior. |

When the host proceeds with a partial set, only the healthy servers appear
in the generated MCP configuration (section 3.5). Failed servers are absent
from the configuration but retain their structured log records in the host
log file.

#### 3.5 Logging

The host writes a per-invocation log file to `$XDG_LOG_HOME/clown/`,
defaulting to `$HOME/.local/log/clown/` when `$XDG_LOG_HOME` is unset or
empty (per the XDG Base Directory Specification). Filenames have the form
`clown-plugin-host-<UTC-timestamp>-<pid>.log`, so each invocation is
isolated.

Records use Go's `slog` text format (`key=value`, one record per line) and
cover:

- Startup arguments, resolved plugin directories, and the effective
  skip-failed policy
- Server start, handshake fields, health-check outcome, and graceful or
  forced shutdown
- Every line of managed-server stderr, tagged with the server name (also
  still mirrored to the host's stderr with a `[plugin/server]` prefix)
- Signals received and forwarded to the downstream command, and the
  downstream's exit status

The host prints the resolved log path to stderr on startup so a concurrent
`tail(1)` is easy to start. The XDG spec permits unbounded growth and
guarantees log deletion does not affect correctness; the host does not
rotate or prune files.

#### 3.6 MCP Configuration

Once all servers are healthy, the host generates a temporary MCP
configuration file:

```json
{
  "mcpServers": {
    "<plugin-name>/<server-name>": {
      "url": "http://127.0.0.1:<port>/mcp"
    }
  }
}
```

- Server names use the format `<plugin-name>/<server-name>`
- For `streamable-http` servers, the URL path is `/mcp`
- For `sse` servers, the URL path is `/sse`

The file is passed to the downstream command via `--mcp-config`.

#### 3.7 Downstream Execution

The downstream command (typically `claude`) is started as a child process
with the `--mcp-config <temp-file>` flag prepended to its arguments.
`SIGTERM` and `SIGINT` received by the host are forwarded to the downstream
process.

The host waits for the downstream command to exit, then proceeds to shutdown.

#### 3.8 Shutdown

When the downstream command exits:

1. `SIGTERM` is sent to each server's process group
2. The host waits up to 5 seconds for each server to exit
3. If a server has not exited after the grace period, `SIGKILL` is sent to
   its process group
4. The temporary MCP configuration file is removed
5. The host exits with the downstream command's exit code

### 4. Architecture Integration

The command chain with `clown-plugin-host`:

```
clown (shell wrapper)
  └─ exec clown-plugin-host --plugin-dir A [--skip-failed] -- claude [args]
       ├─ open $XDG_LOG_HOME/clown/clown-plugin-host-*.log
       ├─ [no clown.json found] → exec claude directly (pass-through)
       └─ [clown.json found]
            ├─ launch HTTP server children (parallel)
            ├─ read handshake + poll healthz for each
            ├─ collect per-server start report
            ├─ [any failures] → skip / prompt / abort (§3.4)
            ├─ generate temp .mcp.json for healthy servers
            ├─ run claude as child with --mcp-config
            ├─ forward signals
            ├─ wait for claude to exit
            ├─ SIGTERM servers → grace period → SIGKILL
            └─ exit with claude's exit code
```

The shell wrapper continues to handle TTY cleanup after `clown-plugin-host`
exits, and recognizes `--skip-failed` as a clown-level flag that is
forwarded into `plugin_host_args`. The `--plugin-dir` flags are also passed
to claude for stdio plugin loading; `clown-plugin-host` only acts on plugin
directories that contain `clown.json`.

## Security Considerations

### Local-only Binding

Servers MUST bind to `127.0.0.1` (loopback only). The handshake protocol does
not support remote addresses. This ensures MCP servers are not exposed to the
network.

### Process Isolation

Each server runs in its own process group. This provides:

- Clean signal delivery (the entire server process tree receives SIGTERM)
- Reliable cleanup (SIGKILL reaches all child processes)
- Isolation between servers (one server's crash does not affect others)

### Trust Model

Servers are local commands shipped within trusted plugin flake outputs. They
inherit the same trust level as any other code in the plugin — they can read
files, make network requests (if permitted), and serve MCP tools that
influence model behavior.

Clown's managed-settings guardrails (Bash disabled, auto-mode disabled)
apply regardless of which HTTP MCP servers are running. Servers cannot
override managed settings.

### Stdio Channel

The server's stdout is consumed by the host for the handshake line.
Servers MUST NOT write additional data to stdout after the handshake.
Diagnostic output MUST go to stderr, which the host forwards with a prefix.

## Compatibility

This specification introduces a new manifest (`clown.json`) and binary
(`clown-plugin-host`) without modifying any existing interfaces:

- Plugins without `clown.json` are unaffected (exec pass-through)
- Existing `.mcp.json` / `plugin.json` stdio servers continue to work
- The `--plugin-dir` flag semantics for Claude Code are unchanged
- The `mkCircus` interface is unchanged

A plugin MAY ship both `clown.json` (for HTTP servers) and `.mcp.json`
(for stdio servers) simultaneously.

## References

### Normative

- [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin) — Handshake
  protocol inspiration
- [Claude Code MCP transports](https://docs.anthropic.com/en/docs/claude-code) —
  streamable-http and sse transport documentation
- [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) — Requirement keyword
  definitions

### Informative

- [RFC 0001: Parameterized Plugin Loading](0001-parameterized-plugin-loading.md) —
  Plugin directory structure and `mkCircus` interface
- [clown-plugin-host(1)](../../man/man1/clown-plugin-host.1) — Host binary
  man page
- [clown-json(5)](../../man/man5/clown-json.5) — Manifest format man page
- [clown-plugin-protocol(7)](../../man/man7/clown-plugin-protocol.7) —
  Protocol overview man page

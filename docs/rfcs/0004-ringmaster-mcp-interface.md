---
status: draft
date: 2026-04-24
---

# Ringmaster MCP Tool Interface

## Abstract

This specification defines the `ringmaster` MCP (Model Context Protocol) server, a clown-internal MCP server that exposes one tool per sandboxed subagent declared in the active subagents catalog. Tool handlers generate an `__impure` Nix derivation for each invocation, run it with `nix build`, and return a reference to `$out` — the realized workspace the agent left behind. Ringmaster is the universal cross-harness dispatch surface for sandboxed subagents — Claude Code, Codex, Crush, and OpenCode all consume it through their standard MCP client paths.

## Introduction

FDR-0001 specifies that sandboxed subagents dispatch through an MCP tool surface rather than through Claude Code's `--agents` JSON interface. The reasoning is in ADR-0001: `--agents` is Claude-specific, dispatches in-process (so the sandbox boundary is wrong), and does not generalize to Codex or Crush. MCP is the one interface every harness already supports.

Ringmaster is the MCP server that implements this dispatch. It runs in the clown-plugin-host process tree (or stdio-forked from clown itself), discovers sandboxed subagents at startup, and advertises one tool per subagent in its `tools/list` response.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be interpreted as described in RFC 2119.

## Specification

### 1. Server Identity and Registration

Ringmaster MUST register itself in the compiled plugin manifest as a regular MCP server with `name = "ringmaster"`. It SHOULD be invisible to consumers who have declared no sandboxed subagents — if the subagents catalog contains zero entries with `sandbox.enabled = true`, clown MUST NOT launch the ringmaster server.

Ringmaster MAY be implemented as either:

- A stdio MCP server invoked via `clown ringmaster` (consistent with the existing plugin-protocol conventions).
- An HTTP MCP server over clown's plugin-protocol handshake (RFC-0002), launched by `clown-plugin-host`.

The HTTP path is RECOMMENDED because it supports `notifications/tools/list_changed` (needed when the subagents catalog is hot-reloaded) and server-initiated progress notifications during long-running `nix build` invocations.

### 2. Tool Naming

For each sandboxed subagent with `name = "<Name>"`, ringmaster exposes one tool named `run_<snake_case_of_name>`. For example, a subagent named `TestFixer` becomes the tool `run_test_fixer`.

Tool names MUST match the regex `^run_[a-z][a-z0-9_]*$`. Ringmaster MUST reject subagent definitions whose transformed name does not match and MUST surface this as a build-time error in clown.

### 3. Tool Input Schema

Each tool accepts the following JSON input:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `prompt` | string | Yes | The task description for the subagent. Becomes `stdin` or `--prompt-file` input depending on backend. |
| `workspace_ref` | string | No | Reference to the workspace the subagent should operate on. See §3.1. |
| `context` | object | No | Additional structured context passed as environment or config file. |
| `extra_tools` | list of string | No | Tools permitted for this specific invocation, merged with the subagent's base `tools`. Subject to build-time validation: each must already be present in the subagent's Nix closure. |

#### 3.1 Workspace References

A `workspace_ref` is one of:

- A literal absolute path on the host. Ringmaster MUST verify the path is inside a declared "allowed workspaces" list (configured per-clown-install, default: the parent agent's `$PWD` as discovered at ringmaster startup).
- A store path (content-addressed). Used when the parent agent wants to operate on a specific historical snapshot.
- The literal string `"$PWD"`, which resolves to the ringmaster server's `$PWD` at startup (the parent agent's initial working directory).

When omitted, `workspace_ref` defaults to `"$PWD"`.

### 4. Tool Output Schema

Each tool returns a JSON object:

```json
{
  "status": "success" | "timeout" | "agent_error",
  "exit_code": 0,
  "out_ref": "/nix/store/<hash>-<name>-out",
  "invocation_id": "<uuid>",
  "duration_ms": 12345
}
```

- `status` MUST be one of the three enumerated values. `success` requires the subagent's exit code to be 0.
- `out_ref` is the store path of the realized derivation output. The parent agent can read, diff, or rsync from this path. Whatever the subagent wrote to `$PWD` during its run is present here; there is no prescribed layout.
- `invocation_id` is a UUID the ringmaster generates per call, used in broker audit logs and transcript filenames on the host side.

### 5. Invocation Lifecycle

For each tool call, ringmaster performs the following steps in order.

1. **Validate the input.** Reject early if `prompt` is empty, `workspace_ref` is outside the allowlist, or `extra_tools` references tools not in the subagent closure.
2. **Generate invocation ID.** A UUID v4.
3. **Snapshot the workspace.** Create a content-addressed snapshot. Implementations SHOULD use `cp -al` (hardlink copy) for speed, falling back to `cp -a` if cross-device.
4. **Spawn the egress broker** per RFC-0005 when the subagent's `sandbox.egress.allow` is non-empty. Wait for handshake. Record the broker's port and CA bundle path. When allow is empty or `broker = "none"`, skip broker spawn.
5. **Generate the `__impure` derivation.** Its builder seeds `$out` with the workspace snapshot, then invokes `sandcastle` with the sandbox policy per ADR-0005, binding `$out` as the agent's `$PWD`, binding the broker CA and port into the environment (when present), and exec'ing the agent binary.
6. **Invoke `nix build`** with `--impure` and a wall-clock timeout matching the subagent's `timeout_seconds`. Capture stdout and stderr on the host side.
7. **On completion**, resolve `$out`'s store path.
8. **Tear down the broker** (if spawned). Send SIGTERM, wait up to 5 seconds, then SIGKILL. Delete the broker's working directory.
9. **Return the result** per §4.

### 6. Failure Modes

| Mode | Status | Cleanup |
| --- | --- | --- |
| `nix build` exits non-zero before the agent runs (build error in the derivation itself) | `agent_error` with `exit_code = <build exit>` | Broker (if any) is torn down normally. |
| Agent exits non-zero | `agent_error` with `exit_code = <agent exit>` | Partial `$out` may still be returned if Nix realized it. |
| Wall-clock timeout hit | `timeout` | Ringmaster sends SIGTERM to the nix-build process group, waits 10s, SIGKILL. Broker torn down. |
| Broker spawn fails | `agent_error` before dispatch | No sandbox is ever entered. |

Ringmaster MUST NOT retry automatically on any failure. Retries are the parent agent's responsibility.

### 7. Concurrency

Ringmaster MUST support concurrent invocations of the same subagent (different workspaces, different prompts). Each invocation has its own broker instance, its own invocation ID, its own derivation, and its own `$out` store path.

Implementations SHOULD bound concurrency via a server-level semaphore (default: 4 concurrent invocations) to prevent resource exhaustion. When the semaphore is full, further calls block up to a configurable timeout (default: 60 seconds) before returning `agent_error`.

### 8. Hot Reload

When the subagents catalog changes (e.g. a clown rebuild), ringmaster SHOULD emit `notifications/tools/list_changed` to any connected MCP clients. The new catalog is read from the injected `CLOWN_AGENTS_JSON` path.

Hot reload MUST NOT cancel in-flight invocations. New invocations use the new catalog; existing ones run to completion with the catalog they started with.

### 9. Security

Ringmaster itself is NOT sandboxed. It runs as the clown user with access to the Nix daemon socket, the host filesystem (to read workspaces), and the ability to spawn egress broker instances. This is intentional: ringmaster is trusted infrastructure; the sandbox boundary is one layer deeper, inside each derivation it generates.

Ringmaster MUST NOT accept tool calls from sandboxed subagents. Its MCP server is exposed to the top-level agent only, not inside any sandbox. This is the mechanical enforcement of the "one level deep" rule in FDR-0001.

### 10. Observability

Ringmaster writes a structured log line per invocation to `${XDG_STATE_HOME}/clown/ringmaster/invocations.jsonl` containing at least: invocation ID, subagent name, start time, duration, status, exit code, broker port (if any), `$out` store path, workspace snapshot hash.

The agent's own transcript (stdout/stderr captured on the host side outside the sandbox) and any broker audit log are written alongside in a per-invocation subdirectory.

## Examples

### Example: Claude Code invoking TestFixer

Parent-side MCP call (abbreviated):

```json
{
  "method": "tools/call",
  "params": {
    "name": "run_test_fixer",
    "arguments": {
      "prompt": "Fix the failing test foo::bar::test_baz in the current workspace.",
      "workspace_ref": "$PWD",
      "context": { "failing_test": "foo::bar::test_baz" }
    }
  }
}
```

Response:

```json
{
  "content": [
    {
      "type": "text",
      "text": "{\"status\":\"success\",\"exit_code\":0,\"out_ref\":\"/nix/store/abc123...-test-fixer-out\",\"invocation_id\":\"8f3b...\",\"duration_ms\":42103}"
    }
  ]
}
```

Claude Code receives the store path, diffs it against the developer's workspace using its normal tooling, shows the changes to the developer for review, and applies them via its normal `Edit` tool with normal permissions. The subagent never touched the real workspace.

## Forward Compatibility

A future v2 MAY add:

- Streaming progress notifications during agent execution (partial transcripts, in-progress file writes).
- A `cancel_invocation` meta-tool for long-running calls.
- Multi-turn conversations with the egress broker as the comms channel.
- Federation: ringmaster in harness A dispatching to ringmaster in harness B.

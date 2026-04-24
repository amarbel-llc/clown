---
status: draft
date: 2026-04-24
---

# Sandboxed Subagent Schema

## Abstract

This specification extends clown's existing TOML-frontmatter subagent schema (files under `subagents/*.md`, parsed by `parseAgent` in `flake.nix`) with a `[sandbox]` block and related fields that describe how the subagent should be dispatched, what tools and egress it is permitted, and what limits bound its execution. The extension is backwards-compatible: subagents without a `[sandbox]` block (or with `sandbox.enabled = false`) continue to dispatch via Claude's `--agents` interface exactly as today.

## Introduction

Clown parses subagents from TOML frontmatter at build time and emits an `agents-json` file consumed by Claude Code's `--agents` flag. The existing fields (`name`, `description`, `tools`, `disallowedTools`, `model`) describe an in-process subagent whose capabilities are enforced by Claude Code's own tool-dispatch logic.

FDR-0001 introduces a second dispatch path in which a subagent invocation is materialized as a Nix derivation inside a `sandcastle`-managed chroot. That path needs additional schema: which agent binary to invoke, what egress to permit, what timeouts to apply. This RFC defines those fields. The derivation output is simply `$out` with whatever the agent wrote to it; there is no declared artifact schema.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be interpreted as described in RFC 2119.

## Specification

### 1. Existing Fields (unchanged)

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `name` | string | Yes | Used as the MCP tool name after `run_` prefix and snake-case conversion |
| `description` | string | Yes | Shown to the top-level agent |
| `model` | string | No | Backend-specific model name (e.g. `sonnet`, `haiku`, a GGUF filename for `circus`) |
| `tools` | list of string | No | Tool allowlist (semantics depend on backend) |
| `disallowedTools` | list of string | No | Tool denylist; merged with provider defaults |

### 2. New Field: `backend`

Declares which agent binary executes the subagent.

| Value | Executable | Notes |
| --- | --- | --- |
| `"anthropic"` | `claude-code` from `pkgs-claude-code` | Default when absent |
| `"codex"` | `codex` from `pkgs-codex` | |
| `"circus"` | `claude-code` pointed at local `llama-server` | No network egress needed |
| `"opencode"` | `opencode` binary | Requires `~/.config/circus/opencode.toml` on host; tokens injected by the egress broker |

When `backend = "circus"`, implementations MUST configure the sandbox with a netns that has no routes and MUST NOT spawn an egress broker. The subagent's `llama-server` endpoint is reached via a unix-domain socket bind-mounted into the chroot.

### 3. New Block: `[sandbox]`

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | When `false`, the subagent dispatches via `--agents` as today. When `true`, dispatch goes through the ringmaster MCP tool per RFC-0004. |
| `timeout_seconds` | int | `600` | Wall-clock timeout on the outer `nix build`. The derivation's own `timeout` attribute is set to this value. |
| `dangerously_skip_permissions` | bool | `true` when `enabled = true`, else `false` | Passed as the corresponding flag to the agent binary. Only meaningful for backends that accept it (Claude Code, opencode). |
| `memory_mb` | int | `4096` | cgroup memory limit on the nix-build subprocess. |
| `cpu_quota` | string | `"200%"` | cgroup CPU quota; two cores by default. |
| `tasks_max` | int | `512` | cgroup pids.max; bounds fork-bombs. |
| `reference_repos` | list of string (flake refs) | `[]` | Read-only repo mounts for the agent to consult as reference material. Not part of the build; for examples / prior art. Pinned by the flake closure. |

### 4. Workspace Semantics

The agent runs with `$PWD = $out`. Ringmaster seeds `$out` before the agent starts with a copy of the caller-supplied workspace (hardlink-copy where filesystems allow). On agent exit, `$out` is finalized as whatever the agent left behind — there is no prescribed output layout.

Implementations SHOULD provide the seeded workspace directly as `$out` so the agent does not need to know about Nix or store paths. The agent sees an ordinary writable directory.

Implementations MUST NOT expose the parent's actual `$PWD` to the subagent. The subagent writes only to the per-invocation copy inside `$out`.

### 5. `[sandbox.egress]` Block

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `allow` | list of string | `[]` | Domain allowlist. Must be exact hostnames; wildcards SHOULD NOT be used in v1. |
| `broker` | string | (none) | The egress broker implementation. `"none"` or empty disables egress entirely (sandcastle policy uses a no-route netns). Any other value MUST resolve to an executable declared in the subagent's closure that speaks the clown plugin protocol (RFC-0002). No default is provided — consumers pick a broker per subagent. |
| `rate_limit_rpm` | int | `60` | Requests per minute, enforced by the broker. |

When `allow = []` or `broker = "none"`, no broker is launched and the sandbox policy uses a netns with no routes. Explicitly setting `broker = "none"` with a non-empty allowlist is a configuration error and MUST fail build-time validation.

### 6. Build-Time Validation

The existing `parseAgent` function in `flake.nix` is extended to:

1. Parse the `[sandbox]` block when present.
2. Verify that `backend` is one of the four enumerated values.
3. Verify that each entry in `sandbox.egress.allow` is a valid hostname per RFC-1035 syntax.
4. Verify that each entry in `sandbox.reference_repos` resolves to a flake input.
5. Emit a validation error at `nix build` time for any violation.

Invalid subagent definitions MUST fail the clown build, not be silently ignored.

### 7. Backwards Compatibility

The existing `discover.md` subagent and any other definitions without a `[sandbox]` block continue to work unchanged. Their `sandbox.enabled` defaults to `false`, which routes them through the legacy `--agents` dispatch path.

### 8. Examples

#### 8.1 Minimal sandboxed test-fixer

```toml
+++
name = "TestFixer"
description = "Runs failing tests and proposes a fix. Fully sandboxed."
backend = "anthropic"
model = "sonnet"
tools = ["Bash(cargo:*)", "Bash(git:*)", "Edit", "Read", "Write", "Glob", "Grep"]

[sandbox]
enabled = true
timeout_seconds = 900

[sandbox.egress]
allow = ["api.anthropic.com"]
+++

You are a test-fixing agent. [...]
```

#### 8.2 Read-only explorer using local model (no egress)

```toml
+++
name = "LocalExplorer"
description = "Read-only codebase exploration via local model. Zero egress."
backend = "circus"
model = "qwen3-0.6b"
tools = ["Read", "Glob", "Grep"]

[sandbox]
enabled = true
timeout_seconds = 300

[sandbox.egress]
broker = "none"
+++

[...]
```

#### 8.3 Legacy in-process subagent (unchanged behavior)

```toml
+++
name = "Discover"
description = "[...]"
tools = ["mcp__plugin_moxy_moxy__folio_glob", ...]
disallowedTools = ["Bash", "Edit", "Write"]
model = "haiku"
+++

[...]
```

No `[sandbox]` block means legacy dispatch. Bit-for-bit compatible with today's behavior.

## Forward Compatibility

Fields not recognized by the current parser MUST be preserved through serialization so that future versions can add fields without breaking round-trip tooling.

A `[sandbox.resources]` block is reserved for future use (GPU pinning, RAM disk mounts, local model weight bind mounts). Implementations encountering this block in v1 MUST ignore it with a warning.

Richer network filters (path globs, method lists, host+path tuples) are reserved for v1 in the `[sandbox.egress]` block. v1 parsers MUST ignore unknown fields in that block with a warning.

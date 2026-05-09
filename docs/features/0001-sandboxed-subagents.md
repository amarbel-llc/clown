---
status: draft
date: 2026-04-24
---

# Nix-Derivation-Sandboxed Subagents

## Abstract

Clown today dispatches subagents in-process via Claude Code's `--agents` JSON interface: the parent agent calls its Task tool, and the subagent inherits ambient filesystem and network capabilities from the parent process. This record specifies an alternative dispatch path in which a subagent invocation is materialized as a Nix derivation. The Nix builder itself supplies the filesystem sandbox; a host-side egress broker mediates network; the subagent's tool closure is narrowed via `nativeBuildInputs`. The agent's working directory *is* the derivation's `$out` — whatever the agent writes becomes the derivation output directly, with no prescribed artifact layout. The top-level agent consumes that output as a store-path reference and integrates it after review. The goal is to permit `--dangerously-skip-permissions` safely for bounded development tasks by making the derivation boundary the permission boundary rather than the agent's own allowlist logic.

## Motivation

Three properties are desirable and currently absent:

First, a **cryptographically verified capability manifest**. Every subagent invocation is pinned to an exact closure: the agent binary, the tools on `$PATH`, the MCP servers declared in the subagent's config, the repo's *dependencies* (transitively, as closure inputs), and — optionally — other repos mounted read-only as reference material (examples, prior-art code, design-doc corpora). The derivation's `.drv` hash is a commitment to "this agent, these tools, this prompt, this context, these reference repos." Audit trails become enumerations of store paths. Clown already has the ingredients: TOML (Tom's Obvious Minimal Language) frontmatter parsed by `builtins.fromTOML`, Nix-built tool closures, and build-time baking via ldflags. What is missing is using that closure as the execution boundary rather than merely the source-of-truth for flags passed to an in-process dispatcher.

Second, **kernel-enforced filesystem confinement via the Nix builder sandbox, composed with a host-side egress broker for network**. The Nix builder sandbox on both darwin (`sandbox-exec`) and linux (namespaces + a small seccomp filter) already denies writes outside `$out` and reads of arbitrary user files. Empirical verification (`zz-pocs/0001` on darwin under `__impure`) confirmed that sensitive paths — the real user's home, SSH private keys, arbitrary host files — are blocked, while `$out`, `/nix/store` (read-only), and per-invocation scratch are writable/readable as expected. What the Nix sandbox does *not* enforce is per-subagent network policy, secret injection, or request-level audit. That's what the egress broker (RFC-0005) adds. An earlier version of this design layered `sandcastle` as an additional confinement runtime; that layering proved infeasible (macOS refuses to nest sandbox-exec inside itself) and, on inspection, unnecessary for the cooperative threat model — the Nix sandbox plus the broker covers the ground the extra layer would have. See ADR-0005.

Third, **a cross-harness subagent surface**. None of Claude Code, Codex, Crush, and OpenCode share a subagent format, and none fork to external processes for subagent dispatch. The universal integration point they all support is MCP (Model Context Protocol). Exposing sandboxed subagents as MCP tools makes them callable identically from any harness, with one subagent catalog maintained in clown.

The existing `discover` subagent is a narrower precursor: a strict allowlist of specific MCP tools, no Bash or Edit or Write, explicitly read-only — an attempt to force Claude to use preferred tools during discovery. It highlighted the need for a general subagent framework. The proposed design generalizes beyond it by enforcing capability boundaries at the kernel rather than at the agent's own tool-dispatch logic, which enables the far broader category of subagents that need to *write* — test fixers, refactor agents, formatters — safely.

## Non-Goals

This design is for the **development system**, not the build system. There is no pure-derivation consumer of subagent output; outputs are consumed by the interactive top-level agent and integrated by hand or by a human-in-the-loop review step. The viral-impurity property of `__impure = true` derivations (nothing pure may depend on them) is therefore invisible and fine.

Subagents are **one level deep**. Sandboxed subagents MUST NOT spawn further sandboxed subagents. The constraint is enforced mechanically by not including the ringmaster MCP (Model Context Protocol) server in any sandboxed subagent's tool closure, not merely by policy. This avoids the recursive-nix dependency (which Lix has removed) and bounds the blast radius of a confused subagent.

This design does **not** isolate adversarial code. The threat model is a cooperative subagent that might make mistakes. A model actively trying to exfiltrate secrets or break out of the chroot is out of scope; the appropriate substrate for that is Firecracker or gVisor, not the Nix builder sandbox.

This design does **not** sandbox the top-level agent. Claude Code, Codex, and other harnesses run with their own existing permission models at the top. The sandbox boundary is specifically around delegated subagent work.

## Background and Prior Work

### What fits from clown today

Clown already parses subagent definitions from TOML frontmatter via `builtins.fromTOML`, producing an `agents-json` file that is currently passed to Claude via `--agents`. The schema has fields for `name`, `description`, `tools`, `disallowedTools`, and `model`, and the markdown body becomes the system prompt. Extending this schema with sandbox fields — `sandbox`, `egress`, `backend`, `timeout_seconds`, `reference_repos` — is a schema-level addition that reuses the existing parser. See RFC-0003.

Lifecycle management of HTTP MCP servers is handled by `internal/pluginhost`: discover `clown.json` manifests, spawn servers, read handshakes, poll health endpoints, clean up on shutdown. Spawning a per-invocation egress broker uses the same mechanism — it is one more subprocess with a handshake and a health endpoint. See RFC-0005.

Per-provider safety defaults already exist: Claude gets `--disallowed-tools Bash(*)`, Codex gets `--sandbox workspace-write`. The sandboxed-subagent design adds a sibling path, not a replacement.

The `circus` provider (local llama-server) is already integrated and is especially strong for sandboxed subagents: when the agent calls a local model, network egress is not needed at all, and the broker is not spawned.

### What the Nix builder sandbox actually enforces under `__impure`

We probed this empirically in `zz-pocs/0001` on darwin. Under `__impure = true`:

- Writes to the real user's home, `/usr/local`, `/etc`, and other host paths are blocked. Writes to `/tmp` and similar "succeed" but are remapped to a per-invocation scratch dir; they don't leak to the host.
- Reads of `~/.ssh/id_rsa`, arbitrary user files, and `/Users/<user>/` enumeration are blocked. `$HOME` inside the builder is `/homeless-shelter` (nonexistent).
- Reads of `/etc/hosts`, `/etc/passwd` succeed; neither file contains secrets and both are standard for builds that need DNS or user-id info.
- Loopback TCP to a host-side listener on `127.0.0.1:<port>` works end-to-end via both `curl` and raw `/dev/tcp`. This is the channel the egress broker uses.
- External network is *not* deterministically denied on darwin — `__impure` inherits the host netns. The broker is pointed at via `HTTPS_PROXY`, and the cooperative threat model makes advisory-by-convention acceptable.

For linux the analogous probe is not yet run, but the Nix builder sandbox there is strictly stronger (namespaces + seccomp). We expect confinement to be at least as good.

### Why not existing full-isolation runtimes

Firecracker (E2B, microsandbox), gVisor (Modal), and V8 isolates (Cloudflare Dynamic Workers) are the correct substrates for adversarial code execution. They cost: cold start (~100–200 ms for microVMs), operational weight (running a Firecracker fleet is a real project), and the reproducibility story is delegated to whatever built the OCI (Open Container Initiative) image that boots inside.

For the cooperative-subagent case under development, those costs outweigh the benefit. A Nix builder under `__impure` is <50 ms cold, requires no extra infrastructure, and provides exactly the manifest-and-confinement shape we want. A future FDR can introduce a Firecracker tier as an optional escalation for subagents that need stricter isolation, reusing the schema and ringmaster unchanged.

## Design Overview

The design has three cooperating pieces, each specified in its own RFC.

### 1. Subagent schema (RFC-0003)

The existing TOML-frontmatter subagent schema gains a sandbox block and egress block:

```toml
+++
name = "TestFixer"
description = "Runs failing tests, produces a patch."
backend = "anthropic"
model = "sonnet"
tools = ["Bash(cargo:*)", "Bash(git:*)", "Edit", "Read", "Write"]

[sandbox]
enabled = true
timeout_seconds = 900

[sandbox.egress]
allow = ["api.anthropic.com"]
+++

<system prompt body>
```

Existing non-sandboxed subagents remain unchanged; `sandbox.enabled = false` (the default) preserves today's `--agents` dispatch. The schema is specified in RFC-0003.

### 2. Ringmaster MCP dispatch surface (RFC-0004)

Clown ships a built-in MCP server called `ringmaster` that exposes one tool per sandboxed subagent in the active catalog. Tool signature:

```
run_test_fixer(prompt: string, workspace_ref: string, context: object) -> { out_ref: string }
```

The tool handler: generates an `__impure` derivation with the subagent definition and workspace content bound in; invokes `nix build`; returns a reference to `$out`. The top-level agent reads `$out` as the realized workspace and integrates whatever the subagent produced.

Because the interface is MCP, Claude Code, Codex, Crush, and OpenCode call it identically. This is the universal cross-harness dispatch path. See ADR-0001.

### 3. Egress broker (RFC-0005)

Each sandboxed subagent invocation that declares any `sandbox.egress.allow` entries gets its own egress broker instance spawned on a unique port on the host's `127.0.0.1`. The subagent's environment receives `HTTPS_PROXY=http://127.0.0.1:<port>` and `NIX_SSL_CERT_FILE=<broker-ca>`. The broker enforces the per-subagent domain allowlist, optionally injects API keys from host-side secret storage, rate-limits, and writes an audit log.

The broker implementation is pluggable — any executable satisfying the RFC-0005 contract and speaking the clown plugin protocol (RFC-0002) can fill the role. The default implementation is mitmproxy with a policy-loading addon. The broker is distinct from any MCP tools the subagent may have inside its chroot; MCP servers live on stdio, the broker lives on the network boundary.

When `backend = "circus"` (local llama-server) or `sandbox.egress = {}` the subagent needs no egress at all; no broker is spawned and `HTTPS_PROXY` is unset.

### 4. Confinement composition

- **Filesystem:** Nix's builder sandbox. Kernel-enforced. Subagent writes to `$out` only; reads are limited to `/nix/store`, per-invocation scratch, and a small set of harmless system paths (`/etc/hosts`, `/etc/passwd`).
- **Tools:** closure narrowing via `nativeBuildInputs`. If a binary isn't in the closure, it's not on `$PATH`.
- **Network egress:** via the broker on loopback. Advisory-by-convention on darwin (the subagent may ignore `HTTPS_PROXY`); stricter on linux where the builder has a network namespace.
- **Capability manifest:** the derivation's `.drv` hash.

See ADR-0005 for why this specific composition (without sandcastle or a third confinement layer).

### 5. Workspace and output model

The agent runs with its working directory *being* the derivation's `$out`. From the agent's perspective, `$PWD` is a normal writable directory; it does not need to know about Nix or store paths. Whatever the agent writes to `$PWD` (or reads from `$PWD` as its starting state) is the derivation output.

Ringmaster seeds `$out` before the agent starts with a copy of the caller-supplied workspace (per-invocation, hardlink-copy where filesystems allow). On agent exit, `$out` is finalized as-is. There is no prescribed `patch.diff` / `result.json` / `transcript.jsonl` layout — what the agent produces is what gets stored. If a subagent chooses to write a diff, it does; if it chooses to write a JSON summary, it does; clown doesn't impose.

### 6. MCP inside the sandbox

A sandboxed subagent MAY run its own stdio MCP servers inside the sandbox. These are plain subprocesses, bounded by the same filesystem and network constraints as the agent itself — there is no separate policy surface to regulate. Any HTTP egress those MCP servers attempt goes through the egress broker (the only configured route). Ringmaster does not need to know or care.

## Worked Example

A developer invokes the `TestFixer` subagent from Claude Code with the parent-level prompt: *"The test `foo::bar::test_baz` is failing; fix it."*

Claude Code sees `run_test_fixer` among its tools (provided by the ringmaster MCP server clown spawned at startup) and calls it with the current workspace and the failing-test identifier as context.

Ringmaster's handler:

1. Stamps a per-invocation UUID (Universally Unique Identifier), `$INV_ID`.
2. Spawns an egress broker instance bound to `127.0.0.1:<ephemeral-port>` with the allowlist `["api.anthropic.com"]` and a per-invocation auth token. Reads the clown-protocol handshake to confirm it's up.
3. Generates an `__impure` derivation. Its builder:
   - Initializes `$out` with a copy of the parent's `$PWD` (hardlink-copy where possible).
   - Sets `HTTPS_PROXY=http://127.0.0.1:<port>` and `NIX_SSL_CERT_FILE` pointing at the broker's CA bundle in the closure.
   - Runs the agent binary with `$PWD=$out` and the declared tool closure on `$PATH`.
4. Invokes `nix build` on the derivation with a 900-second wall-clock timeout.
5. On completion, tears down the broker, returns `$out`'s store path to Claude Code.

Claude Code receives the store path, diffs it against the developer's workspace using its normal tooling, shows the changes to the developer for review, and applies them using its normal `Edit` tool with normal permissions. The subagent never had write access to the actual workspace — only to `$out`.

## Security Model

### What is bounded

Filesystem writes outside `$out` are blocked by the Nix builder sandbox (kernel-enforced via `sandbox-exec` on darwin, namespaces on linux). Probed empirically: the subagent cannot write to the real user's home, `/usr/local`, `/etc`, or anywhere else outside its designated scratch. It cannot `rm -rf` the real repository.

Reads of sensitive host files (SSH private keys, arbitrary user files, `/Users/<user>/` enumeration) are blocked by the same mechanism.

Tool availability is exactly the declared `nativeBuildInputs`. `$PATH` is constructed from `lib.makeBinPath [declared tools]`; nothing else exists in the closure.

Resource consumption is bounded by a wall-clock timeout passed to `nix build` and by cgroup limits applied via systemd on the nix-daemon service (memory, CPU, task count) on linux; darwin equivalents TBD.

Credentials are not required to reach the subagent. When the broker does secret injection (optional, post-POC), the subagent's environment contains only a per-invocation placeholder; the broker substitutes the real key host-side.

### What is not bounded (and why we accept it)

- **Advisory-by-convention network policy.** The Nix builder sandbox on darwin does not systematically deny external network under `__impure`. The subagent *can* in principle route around `HTTPS_PROXY` and reach external hosts. The cooperative threat model — the subagent is code we wrote, and it obeys the convention — makes this acceptable. An adversarial model requires a stricter enforcement layer (Firecracker with its own firewall, or gVisor, or a host-level packet filter per invocation).

- **Kernel (linux) / sandbox-exec (darwin) attack surface.** Any OS-level escape in the syscalls the builder permits is a sandbox escape. The cooperative threat model tolerates this.

- **Side channels.** A malicious subagent could leak information via the workspace content itself (smuggling data in commit messages, hidden files). Review discipline at the integration step is the control.

- **macOS external network not denied.** See first bullet. Not a regression — sandcastle wouldn't have provided this either under the previous design, since sandcastle's network model on macOS is proxy-routing, not netns.

## Integration Points

### With existing clown

`internal/pluginhost` gains support for spawning per-invocation egress broker instances in addition to long-lived plugin servers. The clown-protocol handshake format already supports this.

`internal/provider/claude.go` and `codex.go` are unchanged for the non-sandboxed path. A new dispatch path is added for sandboxed subagents, routed through the ringmaster MCP tool, not through `--agents`. See RFC-0004.

The flake exposes the ringmaster MCP server as a buildable derivation; it is auto-loaded like any other plugin via `clown.json`. Consumers who do not define sandboxed subagents pay zero cost.

### With MCP-speaking harnesses (Crush, OpenCode)

Because ringmaster is an MCP server, Crush and OpenCode consume sandboxed subagents identically to Claude Code. Crush needs its MCP config pointed at the ringmaster stdio transport; OpenCode consumes it through its `~/.config/circus/opencode.toml` analog. Harness-specific glue lives in clown's provider adapters, not in ringmaster itself.

## Open Questions

All tagged by target stage:

- **POC / v1**: **Workspace size limits.** Copying a 500MB repo into `$out` on every invocation is expensive. Likely approaches: determinatenix lazy trees, Nix `src` filters, `src = dir` narrowing, overlayfs where available. Worth prototyping before committing to a scheme.
- **v1**: **Network filters in TOML.** Richer-than-allowlist declarative network filters in the subagent's TOML (path globs on HTTP methods, host+path tuples, etc.) — policy expressiveness versus broker implementation cost.
- **v1**: **Linux builder-sandbox parity probe.** `zz-pocs/0001` was run on darwin only. Run the same probes on linux to confirm filesystem confinement, loopback-to-host reachability, and network policy under `__impure`.
- **v2**: **Multi-turn subagent conversations.** Ringmaster calls are single-shot today. The egress broker could serve as a bidirectional comms channel in a future version. Breaks the derivation-as-commitment model; defer.
- **v2**: **Caching strategy.** `__impure` disables all caching, which is correct for iterative dev. A warm restart (same prompt, same workspace hash, same subagent) could legitimately reuse the workspace snapshot and broker instance. Opportunity for optimization.
- **v2**: **Stricter confinement tier.** For subagents that need adversarial-grade isolation, introduce a Firecracker or gVisor backend. Schema, ringmaster, and broker unaffected; only the runtime under `nix build` changes.
- **Nice-to-have, not POC**: Per-invocation auth tokens / broker-side secret injection (real API key never reaches the subagent).

## References

RFC-0003 — Sandboxed Subagent Schema
RFC-0004 — Ringmaster MCP Tool Interface
RFC-0005 — Egress Broker Protocol (per-invocation instance)

ADR-0001 — MCP tool dispatch over `--agents` stub
ADR-0003 — Per-invocation broker instance over shared-broker-with-auth
ADR-0005 — Nix builder sandbox as the confinement layer (no sandcastle)

POCs:

- `zz-pocs/0001` — Nix builder sandbox boundary probes (filesystem, network, loopback).
- `zz-pocs/0002` — Ringmaster MCP dispatch spine (stdio, `nix build`, `$out`-as-workspace).

External:

- Nix manual, "Advanced Attributes" — `__impure`
- `numtide/nix-ai-tools`, `archie-judd/agent-sandbox.nix`
- MCP specification, §7 tool definitions

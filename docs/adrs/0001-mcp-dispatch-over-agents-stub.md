---
status: accepted
date: 2026-04-24
---

# ADR-0001: MCP Tool Dispatch Over `--agents` Stub

## Context

Clown today dispatches subagents via Claude Code's `--agents` JSON interface. Each subagent becomes an entry in a JSON blob passed to Claude; Claude's built-in Task tool then dispatches them in-process. For sandboxed subagents (FDR-0001) the in-process model is wrong — the sandbox boundary needs to be outside the harness, not inside it.

Three dispatch paths were considered.

## Options Considered

### Option A: MCP tool exposed by ringmaster server

A clown-internal MCP (Model Context Protocol) server named `ringmaster` exposes one tool per sandboxed subagent. The parent harness calls the tool; the handler generates a Nix derivation and runs `nix build`; the result is returned as structured JSON. See RFC-0004.

Advantages:

- Works identically across Claude Code, Codex, Crush, and OpenCode — they all speak MCP and none of them need to know about the dispatch mechanism.
- Dispatch surface is a standard MCP server; no harness forking needed.
- Output is structured JSON typed by a declared schema; naturally consumed.

Disadvantages:

- Loses Claude Code's Task-tool UI (the nice progress panel). The parent agent sees an MCP tool call, which progresses but doesn't get the same visualization.
- Ringmaster must manage its own invocation lifecycle including cancellation and progress streaming.

### Option B: `--agents` stub that shells to `clown subagent-run`

The subagent's entry in the `agents.json` blob declares a single tool that shells out to `clown subagent-run <n>`. Claude's Task tool dispatches the stub in-process; the stub fires up the derivation.

Advantages:

- Retains Claude Code's native Task UI.
- Reuses existing `--agents` plumbing.

Disadvantages:

- Claude-Code-specific. Codex has no equivalent; Crush has its own Go-side model; OpenCode uses `.opencode/agent/*.md` similarly to Claude but independently. Cross-harness requires one shim per harness.
- The stub dispatches inside Claude's process, meaning a compromised parent could bypass the stub and invoke the underlying tools directly. The sandbox exists only inside `clown subagent-run`, so the guarantee depends on the parent's cooperation.
- Forces a second dispatch hop (Claude → stub → nix-build), complicating cancellation and progress.

### Option C: New Task tool forking in each harness

Fork Claude Code's Task tool (and the equivalents in each other harness) to route sandboxed subagents to clown. Requires upstreaming changes or maintaining forks of every supported harness.

Rejected without deep evaluation. Maintenance burden is incompatible with clown's position as a thin wrapper.

## Decision

Adopt Option A — MCP tool dispatch via the ringmaster server (RFC-0004).

## Consequences

The parent harness's MCP client becomes the dispatch surface, which means any harness that can consume MCP tools gains sandboxed subagents for free.

Claude Code's Task UI is not used for sandboxed subagents. The cost is accepted — progress visualization can be layered on later through MCP progress notifications if it becomes a bottleneck.

Legacy (non-sandboxed) subagents continue to use `--agents` dispatch. The two paths coexist, selected by the `sandbox.enabled` field in the subagent's TOML (Tom's Obvious Minimal Language) frontmatter.

Ringmaster becomes a critical piece of infrastructure; its failure breaks every sandboxed subagent. It must be tested as part of clown's `just build` gate.

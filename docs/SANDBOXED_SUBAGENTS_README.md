# Sandboxed Subagents Design Docs

This directory contains the design documentation for clown's Nix-derivation-sandboxed subagents, proposed in FDR-0001.

## Read order

Start here:

1. `fdrs/0001-sandboxed-subagents.md` — the main feature design record. Motivation, background, design overview, worked example, security model, non-goals, and open questions (tagged by POC/v1/v2).

Then the three specifications:

2. `rfcs/0003-sandboxed-subagent-schema.md` — TOML frontmatter extensions (sandbox, egress, backend, reference repos, resource limits).
3. `rfcs/0004-ringmaster-mcp-interface.md` — cross-harness MCP tool interface used by every harness (Claude Code, Codex, Crush, OpenCode) to dispatch sandboxed subagents.
4. `rfcs/0005-egress-broker-protocol.md` — launch contract, policy format, and runtime behavior required of the egress broker. Implementation is pluggable; default is mitmproxy with a policy-loading addon.

Then the decisions and their alternatives:

5. `adrs/0001-mcp-dispatch-over-agents-stub.md` — why MCP over `--agents` shell stub or per-harness Task-tool forks.
6. `adrs/0003-per-subagent-broker-instance.md` — why spawn a fresh egress broker per invocation instead of routing through a shared one.
7. `adrs/0005-nix-builder-as-sandbox.md` — why the Nix builder sandbox itself is the confinement layer (no sandcastle, no extra runtime). Backed by empirical probes in `zz-pocs/0001`.

## Proposed numbering and placement in clown

Clown currently has:

- `docs/rfcs/0001-parameterized-plugin-loading.md`
- `docs/rfcs/0002-clown-plugin-protocol.md`

This set continues the RFC sequence at 0003–0005 and introduces a new `fdrs/` subdirectory at 0001 and a new `adrs/` subdirectory at 0001, 0003, 0005. (ADR-0002 and ADR-0004 were dropped after design review — their subjects are no longer load-bearing. ADR-0005 was rewritten after `zz-pocs/0001` disproved the original "sandcastle inside Nix builder" composition.)

## POCs

- `zz-pocs/0001` — Nix builder sandbox boundary probes. Filesystem confinement, network behavior, loopback-to-host-listener reachability. Confirmed filesystem confinement is strong, broker-on-loopback works, sandcastle-inside-builder doesn't.
- `zz-pocs/0002` — Ringmaster MCP dispatch spine. zx-based MCP server over stdio, synthetic flake with `workspace` input override, `$out = $PWD` contract. Proven end-to-end.

## Status

All design docs are `status: draft`. The POCs are green; tracer and v1 work follow the plan in `plans/2026-04-24-sandboxed-subagents-poc-to-v1.md`.

## Next actions

- Wire up mitmproxy as the egress broker (POC-0003 or tracer-stage work). `zz-pocs/0001` proved the loopback-to-host-listener path; now validate with a real HTTP proxy enforcing an allowlist.
- Run `zz-pocs/0001`'s probes on linux to confirm sandbox parity before tracer/v1 targets linux.
- Begin tracer-stage integration per the plan doc.

---
status: draft
date: 2026-04-24
---

# Sandboxed Subagents: POC → Tracer → V1

Staging plan for FDR-0001 (Nix-Derivation-Sandboxed Subagents). See the FDR
and its RFCs/ADRs for the target design; this plan scopes the path to get
there.

## Framing

- **POC** — prove the derivation-based execution model works at all. Thrown
  away after it answers yes/no.
- **Tracer Bullet** — end-to-end thin slice along the real architecture:
  real MCP surface, real ringmaster, real broker. Everything is
  minimum-viable but real code that survives into v1.
- **V1** — hardened, concurrency-safe, multi-backend, broker-integrated,
  TOML-driven, merge-ready.

## Key choices

| Decision | Answer |
| --- | --- |
| Confinement composition | Nix builder sandbox + egress broker + closure-narrowed `nativeBuildInputs`. No sandcastle. Per ADR-0005, backed by `zz-pocs/0001` probes. |
| Broker implementation | `mitmproxy` with an addon that loads per-invocation policy from ringmaster's policy file. Audit log via mitmproxy's own logging. |
| Tracer subagent | `discover`. Already exists, already read-only, already has a tool allowlist. Tracer adds a sandbox-enabled sibling; legacy `discover` keeps working via `sandbox.enabled = false` default. |
| POC location | `zz-pocs/000N/` in clown repo. Self-contained. Not wired into the flake top-level. |
| Broker in tracer? | Yes — tracer's value is flushing out the real architecture. Deferring mitmproxy wiring to v1 would leave the least-predictable piece unexercised until merge time. |

## POC history (done)

- **`zz-pocs/0001`**: Nix builder sandbox boundary probes. Originally scoped
  as "sandcastle inside a Nix builder" but that composition proved
  infeasible on darwin (sandbox-exec refuses to nest). Rescoped to "what
  does `__impure` *actually* allow and deny?" Confirmed: filesystem
  confinement is strong, loopback-to-host listener reaches from inside the
  sandbox, broker-on-loopback pattern is viable. Drove the ADR-0005 rewrite.

- **`zz-pocs/0002`**: Ringmaster MCP dispatch spine. zx-based MCP server
  over stdio, synthetic flake with `workspace` input override, `$out =
  $PWD` contract. End-to-end green: Claude Code can invoke `run_discover`
  via MCP, ringmaster dispatches through `nix build`, returns an `out_ref`
  with the seeded workspace. Survives to v1 largely unchanged.

## Stage 1.5: Broker POC (pending)

Goal: validate mitmproxy-as-egress-broker before wiring it into the tracer.

Proposed `zz-pocs/0003`:

- Host-side mitmproxy configured with a hard-coded allowlist (e.g. only
  `api.anthropic.com`).
- Derivation under `__impure` with `HTTPS_PROXY=http://127.0.0.1:<port>` and
  a CA bundle in its closure.
- Probe script inside the derivation does three curls:
  1. `curl https://api.anthropic.com/...` → expected: reaches the broker,
     broker forwards, log shows it.
  2. `curl https://example.com` → expected: broker denies, curl sees 403.
  3. `curl --noproxy https://example.com` → expected: bypasses the broker,
     reaches the external host directly (this demonstrates the
     advisory-by-convention limit from ADR-0005).

Success:

- The allowlisted request reaches the broker and the upstream.
- The denied request is blocked by the broker.
- mitmproxy's audit log shows both.

Non-goals: per-invocation policy file, auto-spawn, handshake protocol.
Tracer exercises those.

## Stage 2: Tracer

Goal: prove the full spine (ringmaster → nix build → agent + broker →
`$out`) works end-to-end on a real subagent, with one minimum
implementation of each piece that survives to v1.

Promoted subagent: `discover`, with `sandbox.egress.allow =
["api.anthropic.com"]` and `backend = "anthropic"`. Exercises the broker.

In-scope:

- Ringmaster MCP server. Currently zx-based from POC-0002; may migrate to
  Go for v1 integration, but tracer can reuse the POC-0002 implementation
  until there's a reason to switch.
- One tool: `run_discover`. Hardcoded in ringmaster (TOML catalog wiring
  not yet hooked up).
- Tool input: `{prompt, workspace_ref}`. Output: `{status, out_ref,
  exit_code, invocation_id, duration_ms}`.
- `workspace_ref = "$PWD"` only.
- Derivation generation: ringmaster writes a synthetic flake per invocation
  (or reuses a template with `--override-input workspace`).
- Derivation uses the Nix builder sandbox directly. No sandcastle. Per
  ADR-0005.
- Workspace seeding: plain `cp -a` into `$out` (hardlink optimization
  deferred to v1).
- **mitmproxy broker**: per-invocation spawn via `clown-plugin-host`.
  Policy file written per RFC-0005. mitmproxy addon loads policy, enforces
  allowlist, terminates TLS with per-invocation CA, audit-logs to
  per-invocation dir. Handshake per RFC-0005 §2.
- Broker teardown on completion (stdin close → SIGTERM → SIGKILL).
- Single-invocation serialization via mutex. No concurrency.

Out of scope (deferred to v1):

- Real TOML parser integration (RFC-0003 §3, §5, §6).
- Multiple subagent definitions from `subagents/*.md`.
- Multiple backends (codex, opencode). Anthropic only.
- Concurrency semaphore.
- Hardlink workspace copy.
- Reference repos.
- cgroup limits.
- Observability beyond per-invocation stderr + broker audit log.
- Hot reload.
- Workspace-size optimization.
- `broker = "none"` fast path.
- Secret injection (tracer passes developer's `ANTHROPIC_API_KEY` through
  mitmproxy; no per-invocation placeholder).

Success:

- `discover` invoked from Claude Code via MCP produces an `out_ref`.
- Sandbox confines filesystem (Nix builder enforces).
- mitmproxy audit log shows the exact requests the agent made; disallowed
  hosts are denied.
- Works on darwin. Linux deferred until the linux sandbox-parity probe
  (open question in FDR-0001) is done.

Survives to v1: ringmaster server, derivation template, mitmproxy broker
spawn/teardown, MCP tool interface, `$out = $PWD` contract.

## Stage 3: V1

Goal: merge into clown master. Everything deferred from tracer, plus
hardening.

In-scope (additions over tracer):

- TOML schema parser extended per RFC-0003 §3, §5, §6. Build-time
  validation in `flake.nix` `parseAgent`.
- All sandbox-enabled subagents in `subagents/*.md` auto-registered as
  ringmaster tools.
- Backends: anthropic, circus, codex, opencode all wired up and exercised.
- Concurrency with semaphore (default 4 from RFC-0004 §7).
- Workspace seeding uses `cp -al` (hardlink copy), with `cp -a` fallback.
- Layered-derivation exploration for workspace seeding
  (determinatenix lazy trees / `src` filters / overlayfs) — picked from
  the POC/v1 open question.
- Reference repos (`sandbox.reference_repos` field, mounted read-only per
  FDR-0001 §1).
- cgroup limits on the outer `nix build` (memory/cpu/tasks from RFC-0003
  §3).
- Observability: `${XDG_STATE_HOME}/clown/ringmaster/invocations.jsonl`
  (RFC-0004 §10); agent transcript + broker audit log captured
  per-invocation.
- Hot reload when the subagent catalog changes (RFC-0004 §8).
- Network-filter extensions in TOML (v1 item from FDR open questions).
- Secret injection in mitmproxy: subagent env gets a per-invocation
  placeholder; mitmproxy substitutes the real key.
- Integration tests: writing subagent, read-only subagent, egress-enabled
  subagent, legacy subagent (backwards compat).
- Linux sandbox-parity probe run and documented.
- Both macOS and linux CI green.

Deferred to v2:

- Multi-turn (broker as comms channel).
- Caching / warm restart.
- Federation.
- `cancel_invocation` meta-tool.
- Firecracker / gVisor tier for adversarial-grade isolation.

Success:

- Merges to clown master. All integration tests pass.
- At least one real developer task completed using a sandboxed subagent.

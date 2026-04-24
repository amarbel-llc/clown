---
status: accepted
date: 2026-04-24
---

# ADR-0003: Per-Subagent Broker Instance Over Shared Broker with Auth

## Context

Each sandboxed subagent invocation needs an egress broker that enforces a per-subagent policy (domain allowlist, rate limit, secret injection). The broker is the single network endpoint reachable from inside the chroot. Two structural choices: one broker per invocation, or one long-lived broker serving all invocations with per-connection authentication.

Note: the broker is a dedicated HTTP proxy process (implementation unspecified by this ADR; see RFC-0005). It is distinct from any MCP servers the subagent may have access to inside the chroot.

## Options Considered

### Option A: Per-subagent broker instance

Ringmaster spawns a fresh broker for each invocation, waits for its clown-protocol handshake, uses it for the lifetime of the derivation, and tears it down on completion.

Advantages:

- Policy is the broker's launch argument — static for the lifetime of the broker, no dynamic routing logic.
- Lifetime matches invocation lifetime cleanly; no reference counting, no stale state between invocations.
- Broker crash affects exactly one invocation.
- Matches `clown-plugin-host`'s existing lifecycle model (spawn, handshake, health-check, teardown). No new primitives needed.
- Per-invocation CA (Certificate Authority) is generated per broker instance and never outlives the invocation; cert reuse is not a concern.

Disadvantages:

- Higher startup cost: ~100-200 ms per invocation to spawn and handshake. For very short subagents this is a meaningful fraction of wall-clock time.
- Resource usage is O(concurrent invocations) rather than O(1).

### Option B: Shared broker with per-connection authentication

One long-lived broker serves all invocations. The subagent's environment contains an auth token; the broker looks up per-token policy on each incoming request.

Advantages:

- Lower per-invocation overhead; broker is already warm.
- Constant memory footprint regardless of concurrency.

Disadvantages:

- Broker surface area grows: it must hold a policy table, handle policy registration/deregistration from ringmaster, and route correctly on every request.
- Per-invocation CA becomes a problem: either one CA trusts all subagents (too broad) or the broker rotates CAs per token (adds complexity on both sides of the TLS — Transport Layer Security — termination).
- A bug in policy routing could leak secrets from one subagent's invocation to another. Per-instance makes this structurally impossible.
- Broker crash affects all in-flight invocations. Restart requires reconstructing all active policies.
- Harder to reason about in the audit log: every log line needs an invocation correlation ID that the broker reliably maintains.

### Option C: Hybrid (shared broker for simple policies, per-instance for secret-injecting policies)

Considered and rejected. The complexity of two code paths exceeds the startup cost savings.

## Decision

Adopt Option A — per-subagent broker instance, per RFC-0005.

## Consequences

The ~100-200 ms startup cost is paid on every invocation. This is acceptable for subagents with minutes-scale runtimes. For very fast subagents (sub-second local-model calls), the cost is a larger fraction but still tolerable; the alternative is running them with `backend = "circus"` and `broker = "none"`, which eliminates the broker entirely.

The broker's policy model stays simple: launch arguments, not a live registration API.

Broker implementations benefit from the simplification — no need to implement a policy registration surface, no need for per-request policy lookup, no need for CA rotation.

Per-invocation CAs are the natural choice since each broker has its own lifetime. Subagents trust a CA bundle that exists only for their invocation; no cross-invocation contamination is possible.

When the warm-start cost becomes a real bottleneck (if ever), the optimization path is a broker pool — pre-warmed broker processes that get re-parameterized and handed off for an invocation, then returned to the pool. This preserves the per-invocation policy isolation while amortizing the startup cost. Not v1.

---
status: proposed
date: 2026-06-13
---

# Session Identity, Addressing, and the clown↔orchestrator Job-Channel Ownership Contract

## Abstract

clown and a worktree orchestrator (spinclass) jointly provide cross-session job
and chat wakeup, but the boundary between them — who owns session identity,
channel addressing, job state, and liveness — has been implicit, and a series of
point bugs (env-var identity leaks, stale "active" sessions, dropped directed
wakes) all traced to that missing contract. This document specifies clown's
side: the canonical session-identity and channel-addressing primitive, the
record-only job/channel/journal model and its deliberate liveness boundary, and
the ownership seam where clown meets an orchestrator. It is the umbrella
contract over the job-wakeup channel, output spool, and job-platform tool
surfaces — it names what each layer owns rather than redefining them.

## Introduction

clown produces a durable, cross-session job-wakeup channel (and the cross-session
chat that rides on it); an orchestrator such as spinclass creates the worktree
sessions that produce to and consume from it. Three recurring bug classes proved
the cross-layer boundary was under-specified:

1. **Directed wakes silently dropped** when a session's resolved key diverged
   from the key a sender addressed — a leaked `CLOWN_SESSION_ID` from a parent
   process armed the wrong channel (clown#135 / spinclass#169).
2. **Stale "active" sessions** — a session listed as live whose process was gone.
3. **The §10 liveness gap** — status reporting `running` for a producer that
   crashed without writing a terminal record.

Each was point-fixed, but the recurrence showed the contract *itself* was
missing. This RFC is clown's normative half of a two-document pair; the
orchestrator's consumer half is **spinclass FDR-0016**. It consolidates and
references the component specifications — RFC-0009 (channel), RFC-0010 (spool +
status), RFC-0011 (MCP tools) — rather than restating them, and pins the seam
those point-fixes kept rediscovering.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Session Identity and Addressing

Session identity is the addressing primitive for the entire channel: a job, a
wake, and a chat message are all routed by a **channel id** derived from a
**session key**.

**Resolution precedence.** clown MUST resolve the active session key in this
order (RFC-0009 §2):

```
CLOWN_SESSION_ID  >  SPINCLASS_SESSION_ID  >  CLAUDE_SESSION_ID  >  generated random
```

`CLOWN_SESSION_ID` is the explicit override and MUST win when set, so a producer
can deliberately address another session's channel. clown MUST NOT silently
invert this precedence (e.g. prefer `SPINCLASS_SESSION_ID`) to compensate for a
leaked value — see Divergence below.

**Channel derivation.** The channel id MUST be the first 16 bytes of
`SHA-256(sessionKey)` rendered as 32 lowercase hex characters. This is the sole
mapping from key to channel; every producer and the monitor MUST derive it
identically. Directed routing succeeds if and only if the recipient's resolved
key produces the same channel id the sender addressed.

**Authoritative read (`whoami`).** clown MUST expose the resolved
`{sessionKey, channelId, source}` for the current session via
`clown job whoami` (clown#135), where `source` names the precedence branch that
won (`CLOWN_SESSION_ID` / `SPINCLASS_SESSION_ID` / `CLAUDE_SESSION_ID` /
`generated`). `whoami` is the authoritative answer to "what channel does this
session resolve to." A consumer SHOULD obtain a session's canonical key/channel
by calling `whoami` rather than recomputing from a nominal key, because the env
in which a monitor/producer resolves can differ from the session's nominal env.

**Key-value origin (the seam).** clown does NOT originate the key *value*. In an
orchestrated session the value is set by the orchestrator (spinclass sets
`SPINCLASS_SESSION_ID`; clown snapshots it into `CLOWN_SESSION_ID` at boot if
unset). clown owns the *resolution, derivation, and `whoami` read*; the
orchestrator owns the value's *origin*.

**Divergence.** When `CLOWN_SESSION_ID` is set AND `SPINCLASS_SESSION_ID` is
present AND they differ, clown SHOULD emit a visible warning (clown#135). It
MUST NOT change the resolution to "fix" it. A divergence is an env-hygiene smell
— an orchestrator leaked an identity across a process boundary — and MUST be
fixed at the source (the orchestrator's child-env construction), not by clown
re-interpreting the precedence.

### 2. Job, Channel, and Journal Model (by reference)

clown owns the following components; their normative detail lives in the cited
RFCs, and a conformant clown MUST implement them:

- **Durable journal + nudge channel** — RFC-0009: per-job JSONL record schema,
  terminal-only wake policy, at-least-once replay/ack, and the `clown job`
  producer CLI.
- **Output spool + status probe** — RFC-0010: the producer-written `.out` spool
  and the journal+spool-derived `clown job status`.
- **Job-platform MCP tools** — RFC-0011: the agent-facing `job_*` tool surface.

This RFC adds no new wire format; it is the umbrella that names these as
clown-owned and binds them to the identity primitive in §1 (every one of them
addresses by the §1 channel id) and the liveness boundary in §3.

### 3. Liveness Semantics and Boundary (record-only)

clown is **record-only**. The journal, `status`, and `whoami` report what was
**recorded**: whether a terminal record exists, the last-activity timestamp, and
the derived state (`running` / `delivered` / a terminal type).

clown MUST NOT infer producer liveness (RFC-0009 §10). A producer that died
without writing a terminal record MUST be reported as `running` with a stale
`last_activity`; clown MUST NOT claim such a producer is alive, and MUST NOT
claim it is dead. Deriving "running" from "no terminal record" is a statement
about the *journal*, never about the *process*.

Consequently clown owns "lifecycle **state as recorded**," not "is the process
alive." An authoritative liveness verdict for a session or job REQUIRES
combining clown's records with the orchestrator's process (PID) liveness;
neither layer is sufficient alone. This combination is part of the seam (§4).

### 4. Ownership Boundary (the Venn, clown side)

**clown OWNS:**

- Message transport and the single journal store (one source of truth on disk).
- Wake and delivery: the monitor, the UDS nudge, replay and per-channel ack.
- The identity-addressing primitive of §1: precedence, `ChannelID`, `whoami`.
- Job lifecycle and **state-as-recorded** per §2/§3.
- The producer and consumer surfaces: `clown job …`, `clown job-mcp`,
  `ringmaster`.

**The SEAM (shared; neither layer alone is authoritative):**

- **The session key.** The orchestrator sets `SPINCLASS_SESSION_ID`; clown
  derives `CLOWN_SESSION_ID` and the channel id. `whoami` is the shared,
  authoritative resolver consumers read.
- **SessionStart / SessionEnd.** clown DECLARES the job-watch monitor (via a
  synthesized built-in plugin) and the harness SPAWNS it; the orchestrator
  materializes its own session/registry state in the same lifecycle. (Monitor
  arming reliability is an open gap — clown#132.)
- **Liveness.** clown's recorded state MUST be combined with the orchestrator's
  PID liveness for an authoritative "is this session/job actually alive" verdict
  (§3).

**The ORCHESTRATOR OWNS** (normative detail in spinclass FDR-0016, summarized
here for boundary clarity):

- Git worktree lifecycle (create / merge / clean).
- The session registry and addressing enrichment: the readable
  `<repo>/<branch>` ↔ canonical-key mapping, descriptions, listings.
- Harness orchestration (spawn / fork / resume), including child-process env
  construction and hygiene — the layer that MUST NOT leak a parent's
  `CLOWN_SESSION_ID` into a child session (spinclass#169).
- PID-based liveness of its own sessions.

### 5. Known Gaps (informative)

These are the open items where the seam is currently leaky. This RFC defines the
contract they MUST converge to; it does not solve them:

- **clown#132** — job-watch monitor auto-arm / singleton / backlog-at-arm.
- **clown#135** — directed-wake divergence + `whoami` + warn-on-divergence
  (the §1 authoritative-read and divergence-warning requirements originate here).
- **clown#136** — env-hygiene hardening (per-MCP-child injection + passing the
  monitor its key explicitly, to shrink the ambient-identity blast radius).
- **spinclass#169** — the orchestrator's spawn-env scrub: the load-bearing fix
  for the directed-wake divergence class.

## Security Considerations

**Trust domain.** The channel is a single-user, single-host facility (RFC-0009).
Any local process may write to a channel's journal or address any channel;
session identity is an **addressing key, not an authentication boundary**.
Conformant implementations MUST NOT treat the channel id or session key as a
secret or capability.

**Channel-id opacity.** The channel id is a one-way hash of the session key, so
the id alone does not reveal the key. This aids the operator addressing model
(`ringmaster --channel <id>` reaches a job whose key the operator does not hold)
but provides no confidentiality: anyone who knows a session key can address its
channel.

**The identity-leak class.** A leaked `CLOWN_SESSION_ID` (the divergence of §1)
causes wakes and chat to route to the wrong session — a worker may receive its
driver's messages, and messages intended for the worker are dropped. The
mitigation is env hygiene at the orchestrator (spinclass#169) plus clown's
divergence warning (clown#135); it MUST NOT be "fixed" by inverting the §1
precedence, which would break legitimate cross-channel targeting. Extending the
channel across host boundaries would require an actual peer-authentication model
and is out of scope (clown#119).

**`whoami` disclosure.** `whoami` discloses only the calling session's own
resolved key/channel — no cross-session disclosure beyond what the addressing
model already permits.

## Conformance Testing

clown implements this contract through the CLI and MCP surfaces, so it is
testable against the binary. Most conformance lives in the component RFCs' suites;
this RFC adds the identity/liveness rows. Tests use binary injection via
`bats-emo`:

    require_bin CLOWN_BIN clown

### Covered Requirements

| Requirement | Test File | Description |
|-------------|-----------|-------------|
| §1, resolution precedence (CLOWN > SPINCLASS > CLAUDE > random) | `zz-tests_bats/job_wakeup.bats` | resolved key follows the §2 precedence |
| §1, `ChannelID = sha256(key)[:16]` hex | `zz-tests_bats/job_wakeup.bats` | producer and monitor derive the same channel id |
| §1, `whoami` reports `{sessionKey, channelId, source}` | `zz-tests_bats/` (pending clown#135) | authoritative resolved read; correct `source` branch |
| §1, divergence warns, does not flip precedence | `zz-tests_bats/` (pending clown#135) | warning emitted; `CLOWN_SESSION_ID` still wins |
| §3, record-only liveness (no producer-liveness inference) | `zz-tests_bats/job_output_spool.bats` | a producer with a `started` record and no terminal reports `running` with stale `last_activity` |

## Compatibility

This RFC consolidates already-shipped behavior (RFC-0009/0010/0011) and
introduces no new or breaking wire/disk format. The `whoami` surface (clown#135)
is additive. The divergence warning (clown#135) is non-behavioral (diagnostic
output only; routing is unchanged). The `SPINCLASS_SESSION_ID → CLOWN_SESSION_ID`
boot snapshot is unchanged. Orchestrator consumers (spinclass FDR-0016) migrate
to calling `whoami` for the canonical key/channel rather than recomputing; that
is a consumer-side change requiring no modification to the component RFCs.

## References

### Normative

- RFC-0009 — Job-Wakeup Channel (`docs/rfcs/0009-job-wakeup-channel.md`):
  session-key resolution (§2), record schema, wake policy, producer CLI, and the
  §10 liveness gap this RFC formalizes as the record-only boundary.
- RFC-0010 — Job Output Spool and Status (`docs/rfcs/0010-job-output-spool-and-status.md`).
- RFC-0011 — Job-Platform MCP Tools (`docs/rfcs/0011-job-platform-mcp-tools.md`).

### Informative

- spinclass FDR-0016 — the orchestrator (consumer) half of this two-document
  contract.
- FDR-0013 — Job-Wakeup Channel feature treatment
  (`docs/features/0013-job-wakeup-channel.md`).
- clown#132, clown#135, clown#136 — open seam gaps (§5).
- spinclass#169 — orchestrator spawn-env scrub (the load-bearing divergence fix).
- clown#119 — cross-host channel exploration (out of scope here).

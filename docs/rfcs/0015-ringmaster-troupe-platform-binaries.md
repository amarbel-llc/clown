---
status: proposed
date: 2026-07-03
---

# ringmaster and troupe Platform Binaries (job-control and messaging CLI surface)

## Abstract

This document specifies two standalone command-line binaries — `ringmaster`
(job control) and `troupe` (messaging) — as the first-class producer and
operator surface for clown's job platform. It promotes the verbs currently
carried by `clown job` and `clown chat` onto dedicated binaries whose names
match the `ringmaster`/`troupe` MCP tool surfaces already established for
agents, and defines the transport-abstract contract `troupe` MUST honor so a
future XMPP backend can replace the current on-disk journal without changing
the surface. The relocation is behavior-preserving — each `ringmaster <verb>` and
`troupe <verb>` reproduces the exit codes, flags, and output of the `clown
<verb>` it replaces — and because clown ships as a single atomic flake it is a
hard cutover: the `clown job`, `clown job-watch`, `clown job-mcp`, and `clown
chat` subcommands are removed outright, with no forwarding shim.

## Introduction

clown#144 split the synthesized `clown-builtin-jobs` MCP server into two
intent-revealing agent-facing surfaces: **troupe** (messaging —
`chat_send`/`chat_read`/`chat_list` and `job_message`) and **ringmaster** (job
control — lifecycle, status, spool, `job_wait`). That split was deliberately
scoped to the MCP surface. The producer/operator vocabulary underneath was left
as `clown job <verb>` and `clown chat <verb>`, and the shared substrate as
`internal/jobwake`. clown#155 raised the resulting asymmetry — agent tools say
`troupe`/`ringmaster`; the CLI and package say `job`/`chat`/`jobwake` — and
asked how far the platform vocabulary should propagate.

This specification resolves clown#155 in favor of carrying the vocabulary down
to the **binary/CLI layer**, and no further. It is the CLI-surface slice of
clown#117 ("clown as the complete job system"): a coherent, standalone
control-plane surface is a prerequisite for the cross-producer `job list`,
dependency sequencing, and resource-limit primitives that epic explores. Two
maintainer decisions anchor the design:

1. **Two binaries, not one.** `ringmaster` and `troupe` are distinct
   front-of-house surfaces mirroring the two MCP servers. `troupe` is
   further constrained: it will eventually use XMPP ([RFC 6120]) as its
   message transport, so its CLI contract MUST be transport-abstract, with
   the current job-channel journal as merely one backend.

2. **Local models are jobs.** `ringmaster` is a single control-plane hat.
   The llama-server lifecycle management it already performs (FDR-0010) is
   modeled as a specialized *kind* of job the platform runs, not a second,
   unrelated concern bolted onto the binary. This unifies "run and supervise
   a llama-server" and "run and supervise a producer job" under one surface.

3. **Hard cutover, no deprecation window.** clown ships as a single atomic
   flake, so the internal consumers of the job CLI — the wake monitor it
   synthesizes and the MCP servers it injects — update in the same build. The
   `clown job*` and `clown chat` subcommands are removed outright rather than
   aliased; external producers cut over in lockstep.

The scope of this RFC is the **surface contract and its migration**: which
verbs live on which binary, the behavior-preservation and transport-abstraction
guarantees, and the deprecation sequence. The deeper unification of llama-server
lifecycle into a typed-job model, and the cross-session job DAG, are out of
scope here and remain clown#117's concern; this document only fixes the surface
those primitives will land on.

### Scope

In scope:

- The `ringmaster` and `troupe` binary boundaries and their verb sets.
- Behavior preservation relative to today's `clown job`/`clown chat`.
- The transport-abstraction contract for `troupe`.
- The migration/deprecation sequence for existing producers.

Out of scope (unchanged by this RFC):

- `internal/jobwake` — the shared substrate keeps its name and API. Both
  binaries sit on it; splitting it would be a false division.
- `clown presence` — stays a `clown` subcommand. spinclass's liveness probe
  (`clown presence list --group <id> --quiet`, spinclass#201) is a freshly
  hardened cross-repo contract; moving it buys nothing and breaks that probe.
- The `troupe`/`ringmaster` MCP surface names (clown#144) — already correct.
- The llama-server-as-typed-job refactor and cross-session DAG (clown#117).

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Binaries

Two binaries constitute the platform surface:

- **`ringmaster`** — job control. This binary already exists as clown's
  FDR-0010 llama-server control plane and already carries the operator job
  verbs (`ls`, `status`, `tail`, `cancel`) added in clown#124. This RFC
  extends it with the producer/agent job verbs currently at `clown job`.
- **`troupe`** — messaging. A new binary carrying the verbs currently at
  `clown chat`.

Both binaries MUST be built and installed as part of clown's `symlinkJoin`
output so they resolve on `PATH` for any consumer that has clown installed.

### 2. `ringmaster` verb set

`ringmaster` MUST provide the following verbs. Producer/agent verbs are those
promoted from `clown job`; operator verbs already exist on the binary.

Producer/agent verbs (promoted from `clown job <verb>`):

- `ringmaster start`
- `ringmaster progress`
- `ringmaster done`
- `ringmaster read`
- `ringmaster status`
- `ringmaster wait`
- `ringmaster spool-path`
- `ringmaster whoami`

Operator verbs (already present, retained):

- `ringmaster ls` (`--all` spans channels)
- `ringmaster status`
- `ringmaster tail` (`-f` follow)
- `ringmaster cancel`

Control-plane verbs (already present, retained): the FDR-0010 llama-server
lifecycle subcommands (`list`, launch/stop, model management).

For every producer/agent verb, `ringmaster <verb>` MUST accept the same flags,
produce the same stdout/stderr, and exit with the same status code as the
`clown job <verb>` it replaces, as of the clown version in which this RFC is
implemented. The rename introduces no behavioral change to these verbs.

Where the operator `status` verb and the promoted producer `status` verb
overlap, they MUST resolve to a single implementation with identical behavior
(both already render the RFC-0010 journal+spool-derived status).

### 3. `troupe` verb set

`troupe` MUST provide:

- `troupe send` (from `clown chat send`)
- `troupe read` (from `clown chat read`)
- `troupe list` (from `clown chat list`)
- `troupe message` (from `clown job message`)

The first three are promoted from `clown chat <verb>`. The fourth, `message`,
is promoted from `clown job message` — the standalone waking `job_message`,
which is semantically a messaging operation and which clown#144 already placed
in the **troupe** MCP surface. Moving it to `troupe` corrects a placement:
`message` was a `clown job` sub-verb by history, not by concept. `ringmaster`
therefore does NOT carry a `message` verb (§2).

For each verb, `troupe <verb>` MUST accept the same flags, produce the same
stdout/stderr, and exit with the same status code as the `clown chat <verb>`
(or, for `message`, `clown job message`) it replaces, as of the clown version
in which this RFC is implemented. In particular, `troupe send` MUST continue to
accept the git-commit-format `--message` (summary, blank line, body) and the
explicit `--subject`/`--body` pair, and MUST reject a body that follows the
summary without a blank line (the behavior specified for `clown chat send`).

### 4. Transport abstraction (`troupe`)

`troupe`'s current backend is the RFC-0009 job channel (an on-disk journal plus
a UDS nudge). A future backend is XMPP ([RFC 6120]). To keep the surface stable
across that transition:

- The `troupe` CLI contract (verbs, flags, message model, exit codes) MUST NOT
  expose backend-specific concepts that an XMPP transport cannot honor. In
  particular, the surface MUST NOT require callers to supply journal file
  paths, channel-hash internals, or spool offsets.
- A message MUST be addressable by a logical recipient identifier (a session
  key or group id today; a JID or MUC address under XMPP) resolved by the
  backend, not by a caller-supplied storage location.
- The subject/body message model (§3) is the transport-abstract unit and MUST
  be preserved across backends: the summary maps to a lightweight/notification
  payload and the body to the full message payload, whatever the transport.
- Backend selection MUST be an implementation concern, not a required CLI
  argument. `troupe` MAY expose an OPTIONAL backend selector, but MUST default
  to a working backend with no such flag.

These constraints are normative for the CLI surface only. The `internal/jobwake`
substrate remains the concrete backend today; introducing an XMPP backend is
future work and is not specified here.

### 5. Job model note (`ringmaster`)

`ringmaster` treats a supervised llama-server instance as a specialized kind of
job. This RFC does not specify the typed-job schema (that is clown#117 work),
but it fixes the surface implication: any future job-type taxonomy MUST be
reachable through the single `ringmaster` verb set rather than a parallel
command family. Operators and agents MUST NOT need a second binary to observe
or control llama-server jobs versus producer jobs.

### 6. Monitor and MCP entrypoints

clown synthesizes two entrypoints today, currently as `clown` subcommands:
`clown job-watch` (the per-session wake monitor it registers) and
`clown job-mcp --surface <name>` (the injected stdio MCP servers). As part of
this work both MUST move onto the platform binaries, and the `clown job-watch`
and `clown job-mcp` subcommands MUST be removed:

- **Wake monitor → `ringmaster monitor`.** The monitor gets a user-facing verb
  on `ringmaster`. It watches the session's wake channel, delivers wakeups, and
  registers presence. clown MUST synthesize its per-session monitor command as
  `ringmaster monitor --session <key>`, replacing `clown job-watch --session
  <key>`.
- **MCP servers → `ringmaster mcp` / `troupe mcp`.** The injected servers move
  to the owning binary, replacing `clown job-mcp --surface ringmaster` and
  `--surface troupe` respectively.
- **Binary-path resolution.** Because these commands leave clown's own argv,
  clown MUST resolve the `ringmaster` and `troupe` binary paths through
  burned-in build configuration (as it already does for provider binaries), not
  by assuming `PATH`. Since clown ships as a single atomic flake, the burned-in
  paths and the synthesized commands update together — there is no in-clown
  version skew to bridge.

clown MUST continue to register a working monitor and inject the
`ringmaster`/`troupe` MCP servers; only their names and physical home change.

### 7. Examples

Producer emitting job lifecycle (post-migration):

    ringmaster start --label "nix build" --source pre-merge
    ringmaster progress <job-id> --message "evaluating"
    ringmaster done <job-id> --state succeeded

Operator inspecting another session's job by raw channel:

    ringmaster ls --all
    ringmaster status <job-id> --channel <hex>
    ringmaster tail -f <job-id> --channel <hex>

Agent blocking on completion (the clown#154 join):

    ringmaster wait <job-id> --timeout 600

Session wake monitor (synthesized by clown, replacing `clown job-watch`):

    ringmaster monitor --session <session-key>

Messaging, git-commit-format:

    troupe send --target <session-key> --message $'deploy done\n\nall 3 regions green'
    troupe read
    troupe list

Standalone waking message (promoted from `clown job message`):

    troupe message --target <session-key> --message $'build broke\n\nauth tests red on master'

## Security Considerations

This RFC renames and relocates an existing surface; it does not widen the trust
boundary. The following considerations carry over from the underlying channel
(RFC-0009) and are unchanged:

- **Channel addressing.** `ringmaster`/`troupe` MUST validate any
  caller-supplied `--channel` value as hex and reject path-traversal, exactly as
  the current `clown job`/`ringmaster` operator surface does (clown#125). The
  binary rename MUST NOT bypass `internal/jobwake`'s existing `validateJobID`
  and channel-validation choke points.
- **Single-user trust model.** The job channel today assumes a single-user
  host; `ringmaster cancel` remains cooperative (it signals the owning
  producer, it does not kill an arbitrary PID). Promoting the surface to a
  standalone binary does not grant new cross-user authority.
- **XMPP transport (future).** A future `troupe` XMPP backend introduces a
  network trust boundary absent from the journal backend: authentication to the
  XMPP server, TLS for transport confidentiality, and authorization of
  recipients (who may receive a message). Those considerations MUST be specified
  in the RFC that introduces the XMPP backend; this document only reserves the
  surface. The transport-abstraction contract (§4) MUST NOT be read as
  permitting an unauthenticated network backend by default.
- **No secret exposure via forwarding shims.** The deprecation shims (§
  Compatibility) MUST forward arguments verbatim and MUST NOT log message
  bodies or job payloads to any new location.

## Conformance Testing

Conformance tests for this specification live in `zz-tests_bats/`.

Tests use binary injection via `bats-emo`:

    require_bin RINGMASTER ringmaster
    require_bin TROUPE troupe

### Covered Requirements

| Requirement | Test File | Description |
|-------------|-----------|-------------|
| §2, MUST (verb parity) | `ringmaster_platform.bats` | Each `ringmaster <verb>` accepts the same flags and exit codes as the `clown job <verb>` it replaces |
| §2, MUST (operator verbs retained) | `ringmaster_platform.bats` | `ls --all`, `status --channel`, `tail -f`, `cancel` behave as before the rename |
| §3, MUST (verb parity) | `troupe_platform.bats` | Each `troupe <verb>` matches the `clown chat <verb>` it replaces |
| §3, MUST (message model) | `troupe_platform.bats` | git-commit-format `--message` split and no-blank-line rejection preserved |
| §4, MUST NOT (no backend leakage) | `troupe_platform.bats` | `troupe send`/`read`/`list` require no journal path, channel-hash, or spool offset from the caller |
| §3, MUST (message replacement) | `troupe_platform.bats` | `troupe message` matches `clown job message`; `ringmaster` has no `message` verb |
| §6, MUST (entrypoint relocation) | `ringmaster_platform.bats` | clown synthesizes `ringmaster monitor --session` and injects `ringmaster mcp`/`troupe mcp` |
| §Compat, MUST (removal) | `cutover.bats` | `clown` exposes no `job`, `job-watch`, `job-mcp`, or `chat` subcommand after cutover |
| §Security, MUST (channel validation) | `ringmaster_platform.bats` | A non-hex or traversal `--channel` is rejected |

## Compatibility

### Existing consumers

The removed surface is consumed by:

- **clown itself** — the synthesized wake monitor (`clown job-watch`) and the
  injected MCP servers (`clown job-mcp`), both internal to the flake (§6).
- **External producers** — spinclass and moxy shell out to `clown job` and
  `clown chat` to emit job lifecycle events and messages.
- **Docs and tooling** — `clown-job(1)`, `clown-chat(1)`, the bats suites, and
  the fish completions.

### Cutover (no deprecation window)

clown ships as a single atomic flake, so its internal consumers (the monitor
and MCP injection, §6) are rebuilt and rewired in the same change that
introduces `ringmaster`/`troupe`. There is no in-clown version skew to bridge,
and therefore no clown-side compatibility shim:

- The `clown job`, `clown job-watch`, `clown job-mcp`, and `clown chat`
  subcommands MUST be REMOVED in the same change that adds the `ringmaster`
  producer verbs, `ringmaster monitor`, `ringmaster mcp`, the `troupe` binary,
  and `troupe mcp`. No forwarding shim or alias is retained.
- The relocation is behavior-preserving (§2, §3), so the cutover is mechanical:
  each moved verb behaves identically to the removed one.
- **External producers (spinclass, moxy) MUST be updated in lockstep** to call
  `ringmaster`/`troupe`. Because clown ships no compatibility alias, this is a
  coordinated hard-swap cutover (accept-the-window) — the same pattern used
  when the spinclass chat surface was deleted in favor of clown-owned chat
  (FDR-0017).
- `clown-job(1)`/`clown-chat(1)` are replaced by `ringmaster(1)` and a new
  `troupe(1)`; the bats suites and fish completions are updated in the same
  change.

### Versioning

The behavior-preservation guarantees (§2, §3) are pinned to the clown version
in which the cutover lands. Any behavioral change to a moved verb after the
cutover is a change to the `ringmaster`/`troupe` contract and MUST be evaluated
against this RFC, not silently inherited from a prior `clown job`/`clown chat`
edit.

### Resolution of clown#155

Adoption of this RFC resolves clown#155: the vocabulary is carried to the
binary/CLI layer (`ringmaster`, `troupe`), the substrate (`internal/jobwake`)
and `clown presence` are explicitly held back, and the migration path is
specified above. clown#155 SHOULD be closed as resolved-by-this-RFC once the
RFC is accepted.

## References

### Normative

- [RFC 2119] — Key words for use in RFCs to Indicate Requirement Levels.
- [RFC-0009] — Job-Wakeup Channel (`clown job`). The substrate whose channel,
  journal, and validation semantics the promoted verbs inherit.
- [RFC-0010] — Job Output Spool and Status Probe. Backs the `status`, `tail`,
  and `spool-path` verbs.
- [RFC-0011] — Job Platform MCP Tool Surface. Establishes the `ringmaster` and
  `troupe` surface names this RFC aligns the CLI to.

### Informative

- [RFC 6120] — Extensible Messaging and Presence Protocol (XMPP): Core. The
  future `troupe` transport reserved by §4.
- [RFC-0014] — clown↔spinclass awareness seam. Context for the `clown presence`
  carve-out.
- [FDR-0010] — Ringmaster Control Plane. The existing `ringmaster` binary and
  its llama-server lifecycle role reframed in §5.
- [FDR-0013] — Job-Wakeup Channel feature treatment.
- clown#117 — clown as the complete job system (the epic this RFC is the
  CLI-surface slice of).
- clown#144 — the MCP-surface split that established the `troupe`/`ringmaster`
  names.
- clown#155 — the rename-scope decision resolved by this RFC.
- spinclass#201 — the `clown presence` probe contract held back in Scope.

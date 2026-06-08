---
status: proposed
date: 2026-06-08
---

# Job Platform MCP Tool Surface (`clown job-mcp`)

## Abstract

This specification defines a set of MCP tools — `job_start`, `job_progress`,
`job_done`, `job_message`, `job_read`, `job_status`, and `job_spool_path` — that
clown exposes directly to the running agent so that interacting with the
job-wakeup platform (RFC-0009, RFC-0010) is done through one shared tool
contract rather than each plugin shelling out to the `clown job` CLI or
reimplementing a private job-store, status, and chat surface. clown serves these
tools from a built-in streamable-HTTP MCP server that it injects and manages
through its own plugin protocol (RFC-0002) — clown self-consumes the protocol —
so the tools are lifecycle-managed identically to any third-party plugin server.
Every tool wraps the same `internal/jobwake` operations as the CLI, so the two
front-ends are behaviorally equivalent.

## Introduction

The job-wakeup channel (RFC-0009) and its observability layer (RFC-0010) define
a durable, single-user platform for background-job lifecycle and live status.
Today the only programmatic entry points are the `clown job` CLI subcommands.
Producers reach them by shelling out via `${CLOWN_BIN:-clown} job …`, and the
*agent* has no first-class way to observe or drive jobs at all: it depends on
plugin-specific tools that each reimplement the same ideas over the same
channel — spinclass's `session-job-status` (job status), spinclass's
`chat-send` / `chat-read` (which are already the RFC-0009 `message` channel by
another name), and moxy's `async-result` (job status for async dispatch). The
result is divergent tool surfaces over one platform, and an agent that must
learn each plugin's bespoke vocabulary to ask the same question ("is this job
still alive?").

This RFC specifies a single clown-provided MCP tool surface over the platform.
Because the tools are clown-owned and injected for every applicable session,
any agent gets them without a plugin, and plugins can converge their
agent-facing job tooling onto this contract instead of maintaining private
equivalents. The contract is the tool names, their input and output schemas,
their channel/target semantics, and the conditions under which clown serves
them.

Scope: this document specifies the MCP tool surface and how clown serves it. It
does not redefine the on-disk journal, spool, or nudge formats (RFC-0009,
RFC-0010), nor the `clown job` CLI (which remains the canonical producer and
shell entry point); the MCP tools and the CLI are parallel front-ends over the
same `internal/jobwake` library.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Server identity and serving model

clown MUST serve the job tools from a single built-in MCP server. That server
MUST be a standard clown-protocol (RFC-0002) HTTP MCP server: it binds a
loopback address, speaks the `streamable-http` transport, and emits the
RFC-0002 handshake line

    1|1|tcp|<addr>|streamable-http

on startup. clown MUST inject and manage it through its existing plugin host —
i.e. clown **self-consumes** its own plugin protocol — by synthesizing a
built-in plugin directory containing:

- `.claude-plugin/plugin.json` with a stable `name` of `clown-builtin-jobs`; and
- `clown.json` (version `1`) declaring one `httpServers` entry named `jobs`
  whose `command` is the absolute path to the running clown binary followed by
  the `job-mcp` subcommand (resolved as in `jobMonitorPlugin` /
  `clownExePath()`).

The synthesized directory MUST be passed to the downstream agent via
`--plugin-dir` and removed on shutdown, exactly as the synthesized job-watch
monitor directory is (RFC-0009 §9 implementation). clown MAY combine the job
MCP server and the job-watch monitor into a single synthesized `clown-builtin-jobs`
plugin directory (one `plugin.json` carrying both the `monitors` array and the
`clown.json` server). The plugin host's normal lifecycle — launch, handshake
parse, healthcheck, manifest compilation to a URL-based `mcpServers` entry, and
shutdown — MUST apply unchanged; the built-in server gets no privileged path.

Consequently the tools surface to the agent as plugin-sourced MCP tools under
the `clown-builtin-jobs` / `jobs` identity (e.g. `plugin:clown-builtin-jobs:jobs`),
and tool names are the seven defined in §3.

clown MUST inject the server only for providers that consume `--plugin-dir` and
run the agent as a supervised subprocess (today: `claude`, `clownbox`), matching
`providerUsesPluginDirs`; other providers MUST NOT receive it.

### 2. Session key, target, and the disable switch

Each tool operates on a **channel** resolved from a session key exactly as the
CLI does (RFC-0009 §2). The built-in server inherits the resolved
`CLOWN_SESSION_ID` from the environment clown exports into every plugin server
process, so by default every tool acts on the originating session's channel. A
tool that accepts an OPTIONAL `target` string MUST resolve the channel from
`target` when it is non-empty (a session key, or `*` for the broadcast channel
where the tool permits it), and from the inherited session key otherwise. This
mirrors `clown job … --target`.

When `CLOWN_DISABLE_JOB_WAKEUP=1` (RFC-0009 §8), clown MUST NOT inject the
built-in job server: the agent sees no `job_*` tools at all, mirroring the
disabled job-watch monitor. Producers that require the documented
disabled-channel no-op semantics (emit subcommands exit 0 as no-ops) MUST use
the `clown job` CLI, which preserves them. Because the server exists only when
the facility is enabled, the tools never need an in-band "disabled" return
value; in particular `job_spool_path` always returns a real path (contrast the
CLI, which prints empty on a disabled channel).

### 3. Tools

All tools take a JSON object argument and return a JSON object result. Every
tool that accepts a `job_id` MUST validate it through the same job-id grammar
guard as the rest of the platform (`internal/jobwake` `validateJobID`: RFC-0009
§4 grammar plus the `.`/`..` rejection) before composing any path, and MUST
fail the tool call with an error when validation fails. Each tool wraps the
named `internal/jobwake` operation; its semantics, including durability and
nudge behavior, are those of RFC-0009 and RFC-0010 and are not restated here.

Field tables use `?` to mark OPTIONAL input fields; unmarked fields are
REQUIRED.

#### 3.1 `job_start`

Allocate a job and append the `started` record. Wraps `jobwake.Start`.

| Input | Type | Notes |
|---|---|---|
| `target?` | string | channel override |
| `label?` | string | seeds the generated id |
| `source?` | string | emitting label; defaults per RFC-0009 §8 |

Result: `{ "job_id": string }`.

#### 3.2 `job_progress`

Append a journal-only `progress` record (never wakes). Wraps `jobwake.Progress`.

| Input | Type | Notes |
|---|---|---|
| `job_id` | string | |
| `target?` | string | channel override |
| `message?` | string | newline-flattened (RFC-0009 §4) |

Result: `{ "ok": true }`.

#### 3.3 `job_done`

Append the single terminal record (fsynced), then nudge. Wraps `jobwake.Done`.

| Input | Type | Notes |
|---|---|---|
| `job_id` | string | |
| `state` | string | one of `succeeded`, `failed`, `cancelled`, `interrupted` |
| `target?` | string | channel override |
| `message?` | string | |
| `result_ref?` | string | opaque pointer (data, not auto-executed) |

Result: `{ "ok": true }`. The tool MUST fail when the job already has a terminal
record or `state` is not a terminal type (RFC-0009 §5).

#### 3.4 `job_message`

Emit a standalone waking `message` job. Wraps `jobwake.Message`.

| Input | Type | Notes |
|---|---|---|
| `target` | string | session key or `*` for broadcast (REQUIRED) |
| `message` | string | non-empty (REQUIRED) |
| `from?` | string | sender session key |
| `source?` | string | emitting label |
| `result_ref?` | string | |

Result: `{ "job_id": string }`.

#### 3.5 `job_read`

The pull/observability stream. Wraps `jobwake.ReadJob` (with `job`) or
`jobwake.ScanWaking` (without). Returns records as structured objects, not the
notification-line text.

| Input | Type | Notes |
|---|---|---|
| `job?` | string | one job's full record stream |
| `target?` | string | channel override |
| `since?` | string | RFC 3339 exclusive lower bound on `ts` (channel mode) |
| `type?` | string[] | filter to these event types (channel mode) |

Result: `{ "records": [ <journal record>, … ] }`, each record carrying the
RFC-0009 §4 fields (`v`, `job`, `session`, `source`, `type`, `seq`, `ts`, and
the optional `from`, `message`, `result_ref`). An unknown `job` MUST return an
empty `records` array, not an error (parity with `clown job read --job`).

#### 3.6 `job_status`

Derive a job's status from its journal and spool. Wraps `jobwake.StatusOf`.

| Input | Type | Notes |
|---|---|---|
| `job_id` | string | |
| `target?` | string | channel override |
| `tail?` | integer | trailing spool lines (default 20) |

Result: the RFC-0010 §3 object
`{ state, source, started, ended?, elapsed_sec, last_activity?, spool_bytes,
progress?, tail? }`. When no journal exists for `job_id` the tool MUST fail the
call (RFC-0010 §3: a status of nothing is not a valid answer).

#### 3.7 `job_spool_path`

Resolve and return the producer-written spool path. Wraps `jobwake.SpoolPath`.

| Input | Type | Notes |
|---|---|---|
| `job_id` | string | |
| `target?` | string | channel override |

Result: `{ "path": string }`, the absolute `<job-id>.out` path (RFC-0010 §1).
The tool creates the channel directory but MUST NOT create the spool file.

### 4. Equivalence with the CLI

For every tool in §3, invoking the tool MUST have the same effect on the journal,
spool, and nudge socket as the corresponding `clown job` subcommand with the
same arguments. The MCP surface introduces no behavior the CLI lacks and no
record or wire format beyond RFC-0009 and RFC-0010. An implementation MUST route
both front-ends through one `internal/jobwake` code path.

## Security Considerations

The built-in job server runs at the user's trust level and binds loopback only,
the same posture as any clown-managed plugin MCP server (RFC-0002) and the same
single-user trust domain as the channel itself (RFC-0009 Security
Considerations). It introduces no cross-user surface.

`message` and `result_ref` are emitted into the agent's context and are a
local-trust prompt-injection vector exactly as in RFC-0009; `result_ref` is data
and MUST NOT be auto-executed. `job_message` with `target` `*` can wake every
session, widening the audience but not the trust model (RFC-0009).

Every `job_id` is caller-supplied and is used to compose filesystem paths, so
each tool MUST apply the `validateJobID` guard (§3) — the same traversal
defense as the CLI and library (clown#123). `target` permits cross-session and
broadcast writes; this matches the existing channel trust model and adds no new
boundary.

Because the server is absent when `CLOWN_DISABLE_JOB_WAKEUP=1` (§2), the kill
switch removes the entire MCP surface, not merely its emit side.

## Conformance Testing

Conformance tests for this specification live in `zz-tests_bats/` and in
`internal/` Go tests.

Tests use binary injection via `bats-emo`:

    require_bin CLOWN_BIN clown

The underlying journal/spool/nudge behavior each tool wraps is already
conformance-tested by RFC-0009 (`job_wakeup.bats`) and RFC-0010
(`job_output_spool.bats`); this suite covers the MCP surface and the
serving/injection contract.

### Covered Requirements

| Requirement | Test File | Description |
|-------------|-----------|-------------|
| §1, built-in server injected as an RFC-0002 plugin; tools enumerate | `job_mcp.bats` | a session lists the `clown-builtin-jobs`/`jobs` tools |
| §1, server absent for non-`--plugin-dir` providers | `job_mcp.bats` | codex/opencode/crush sessions expose no `job_*` tools |
| §2, `CLOWN_DISABLE_JOB_WAKEUP=1` suppresses the whole surface | `job_mcp.bats` | no `job_*` tools when disabled |
| §3/§4, each tool's effect equals the CLI's | `internal/.../*_test.go` | handlers route through `internal/jobwake`; journal/spool match the CLI |
| §3, `job_id` validation rejects traversal ids | `internal/.../*_test.go` | invalid id fails the tool call |

## Compatibility

This surface is purely additive. It defines no new on-disk or on-wire format:
the journal record schema (`v: 1`), the spool, and the nudge are unchanged, and
the `clown job` CLI is unmodified. A session in which the built-in server is
absent (disabled facility, or a non-`--plugin-dir` provider) behaves exactly as
before. The MCP tool names and schemas (§3) are the versioned contract; future
additions are additive and consumers MUST ignore unknown result fields.

### Front-end division

The CLI and the MCP tools wrap one library and are equivalent (§4), but they
serve different callers:

- **The agent** calls the MCP tools directly. This is the surface that replaces
  plugin-private agent tools.
- **Producers that are shell processes or non-MCP plugins** continue to use the
  `clown job` CLI, which remains canonical (and is the only front-end that
  preserves the RFC-0009 §8 disabled no-op). Producer plugins are not required
  to migrate emit calls to MCP; an MCP server calling another MCP server's tools
  is not the natural pattern, and the CLI already de-dupes the producer path.

### Result-of-record stays with the producer

The tools migrate the *status and observability* view, not a producer's
**result-of-record** store. A producer keeps whatever holds its domain result —
spinclass's worktree-local `job.json` (the structured TAP result behind
`session-job-status`), moxy's async result blob — and points to it via the
terminal record's `result_ref` (RFC-0009 §4), which `job_read` and the wake line
surface. The end-state layering is therefore: clown owns the journal, status,
spool, and wake; the producer owns its result blob; `result_ref` is the pointer
between them. So `job_status` superseding a plugin's status view never orphans
the result — the agent follows `result_ref` (via `job_read`) to the producer's
own fetch. This does not change `job_status`'s output shape (§3.6): `result_ref`
rides on the terminal record, not in the status object.

### Recommended migration and phase ordering

Designing for both the agent and producer de-dup, the RECOMMENDED ordering is:

1. **Phase 1 — agent-facing observe and control** (`job_status`, `job_read`,
   `job_message`). This is the highest-value de-dup: it gives the agent direct
   job visibility and cross-session messaging, and lets plugins retire their
   private agent tools that reimplement the same channel —
   - spinclass `session-job-status` → `job_status` — the live/graceful status
     view only. Its rendered TAP result rides `result_ref` to spinclass's stored
     `job.json` (see *Result-of-record stays with the producer*). Mind the
     liveness split (RFC-0010 §3): a worker death the producer can detect emits
     `interrupted` and `job_status` reports it, but a hard crash of the serve
     process itself is the RFC-0009 §10 gap — `job_status` (journal-derived-only)
     reports stale `running`, and spinclass's reader-side serve-PID demotion
     stays a native capability (a further reason its tool is a permanent
     dual-path). A future spinclass-side option to make `job_status` agree,
     without a platform PID field: have that reader emit `clown job done --state
     interrupted` for the dead serve, turning its reader-side check into an
     emitter,
   - spinclass `chat-send` / `chat-read` → `job_message` / `job_read` (these are
     already the RFC-0009 `message` channel; two mapping prerequisites — the
     subject/body split and the read cursor — are noted below),
   - moxy `async-result`'s status view → `job_status` (+ `job_read`).
   Plugins SHOULD keep their existing tools during migration so consumers move
   without a flag day. For a plugin that hard-requires clown, the old tool MAY
   become a thin alias and be retired once consumers migrate. But for a plugin
   where **clown is optional** — e.g. spinclass, whose `job.json` is its
   system-of-record and whose `chat-read` polling is its documented clown-absent
   receive path — the native tool is a **permanent** dual-path, not a
   retire-after-deprecation alias: `job_*` is preferred when clown is present
   (server injected), and the native path remains the fallback whenever the
   facility is disabled, clown is absent, or the provider takes no `--plugin-dir`
   (§2). Plugin authors MUST NOT plan a flag day they cannot reach.

2. **Phase 2 — producer surface** (`job_start`, `job_progress`, `job_done`,
   `job_spool_path`). These complete the contract and let the agent drive a job
   end-to-end, and give MCP-native producers an alternative to shelling out.
   Producer plugins MAY adopt them but are not required to; the CLI remains
   supported indefinitely.

A resilience note for Phase 1: `job_status` fails (exit `1` / tool error) for a
job absent from the journal — a producer's locally minted id when the facility
is disabled or clown is absent, or a job whose `job_start` never wrote. A
producer that keeps its own job index therefore remains the resilient fallback
for not-in-journal jobs: the migration makes `job_status` the *primary* status
path, not the *sole* one.

A chat-mapping prerequisite for `chat-send` / `chat-read` → `job_message` /
`job_read`: `chat-send` splits a subject (the ≤200-char wake line) from a body
(full text, recovered on read), whereas `job_message` carries a single
`message`, so the migration needs a documented convention (`message` = the
subject/wake line; body via `result_ref` or a defined field). And `chat-read`
advances an exactly-once per-session read cursor — a different de-dup model from
`job_read`'s `since`/RFC3339 lower-bound scan — so faithful receive needs a
`job_read` cursor mode (the persisted read cursor RFC-0009 §8 marks
not-yet-implemented). Until both land, `chat-*` stays native and only the
`job_status` / `job_read`-for-jobs migration proceeds.

The end state the ordering converges on is a single platform vocabulary: the
agent speaks `job_*` for everything, plugins stop shipping bespoke job tools,
and the channel — not any one plugin — owns job management across the ecosystem.

## References

### Normative

- [RFC 2119] Bradner, S., "Key words for use in RFCs to Indicate Requirement
  Levels", BCP 14, RFC 2119, March 1997.
- [RFC-0002] `docs/rfcs/0002-clown-plugin-protocol.md` — HTTP MCP server
  lifecycle, the handshake line, and manifest compilation that the built-in
  server self-consumes.
- [RFC-0009] `docs/rfcs/0009-job-wakeup-channel.md` — journal schema, session
  key resolution, event types, the nudge, and the disable switch.
- [RFC-0010] `docs/rfcs/0010-job-output-spool-and-status.md` — the output spool
  and the status probe the `job_status` / `job_spool_path` tools expose.

### Informative

- [FDR-0013] `docs/features/0013-job-wakeup-channel.md` — feature-level
  treatment of the channel, including the spinclass-chat migration onto the
  `message` event that `job_message` / `job_read` generalize.
- [clown#122] — job spool + status implementation tracking.

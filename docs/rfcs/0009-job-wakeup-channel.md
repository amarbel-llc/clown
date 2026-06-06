---
status: experimental
date: 2026-06-05
---

# Job-Wakeup Channel (`clown job`)

## Abstract

This specification defines the interface a clown plugin uses to defer a
long-running task to the background and have the originating (or an explicitly
targeted) agent woken by a push notification when the task reaches a terminal
state. The interface is two layers: a durable, append-only on-disk **journal**
that is the at-least-once source of truth for job lifecycle events, and a lossy
**UDS datagram nudge** that provides sub-second wake latency. A clown-spawned
monitor process consumes the journal and emits one line of stdout per *waking*
event, which the harness surfaces to the agent as a notification. This RFC pins
down the journal record schema, the nudge wire format, the socket and path
conventions, the channel-key resolution, the event-type registry with its wake
policy, and the at-least-once replay semantics.

## Introduction

clown plugins that run work exceeding the MCP client's per-server request
timeout (for example spinclass's async `merge-this-session` / `check-this-session`,
or moxy long-running tool calls) today have no way to *push* a "job done /
failed" signal back to the agent: the agent must poll a status tool or hold a
blocking call open. Each such plugin reinvents the same machinery — a job store,
a completion channel, and an ad-hoc way to surface terminal state (clown#110,
spinclass#104).

This RFC specifies a shared facility that clown provides and any plugin may use.
The design separates **correctness** (a durable journal) from **liveness** (a
lossy socket nudge) so that the strict requirement — never lose a wakeup — is
met by disk durability and replay, while low latency is met by a best-effort
datagram that is always free to drop. The notification surface itself is the
existing Claude Code monitor mechanism (a plugin-declared long-running command
whose every stdout line becomes an agent notification); see FDR-0013 for the
feature-level treatment and the spinclass-chat migration that validates the
abstraction.

Scope: this document specifies the wire and on-disk contract and the CLI
conformance surface. It does not specify the harness's internal notification
plumbing, nor any particular plugin's job-store implementation.

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Components and Roles

- **Producer** — a plugin process that creates a job and emits lifecycle events.
  Producers interact with the channel through the `clown job` CLI (§8) or, for
  Go plugins, an OPTIONAL library that writes the identical on-disk and on-wire
  formats. The on-disk journal (§4) and the nudge datagram (§6) ARE the contract;
  any producer that writes them conformantly is valid.
- **Monitor** — a long-running process spawned by clown for the session
  (`clown job-watch`, §8). It binds the channel's nudge socket, replays unacked
  waking events from the journal, and emits one stdout line per waking event.
- **Channel** — the durable journal directory plus the nudge socket for one
  session key (§2, §3).

### 2. Session Key and Channel Identity

A **session key** is a non-empty UTF-8 string naming the agent session a wakeup
is delivered to. The session key of the currently running agent MUST be resolved
in this order:

1. The `CLOWN_SESSION_ID` environment variable, if set and non-empty.
2. Otherwise the `SPINCLASS_SESSION_ID` environment variable, if set and
   non-empty (the spinclass `<repo>/<branch>` key).
3. Otherwise a clown-generated value: the Claude Code session UUID when
   available, else a random 128-bit value rendered as 32 lowercase hex digits.

clown MUST export the resolved value as `CLOWN_SESSION_ID` into the environment
of every plugin MCP server process it launches and into the monitor process, so
producers and the monitor agree on the default channel without further
configuration.

clown SHOULD also export `CLOWN_BIN`, the absolute path to the running clown
binary, into the same environments. Plugin producers that shell out to the CLI
(§8) SHOULD locate it via `${CLOWN_BIN:-clown}` so they invoke `clown job`
reliably regardless of `PATH` (a plugin's nix-wrapped `PATH` need not contain
clown). `CLOWN_BIN` MUST name a binary that accepts the `job` and `job-watch`
subcommands.

A **channel id** is the filesystem-safe identifier derived from a session key:

```
channel-id = lowercase-hex( SHA-256( session-key ) )[0:32]
```

(the first 16 bytes of the SHA-256 digest, rendered as 32 hex digits). The
channel id MUST be used for all path components (§3); the human-readable session
key MUST be carried verbatim in each journal record's `session` field for
debuggability.

A producer targeting the originating session uses the resolved session key. A
producer MAY target another session by passing that session's key explicitly
(`clown job start --target <key>`); the channel id is then derived from the
target key. There is no broadcast form in v1.

### 3. Paths

Journal (durable, survives reboot):

```
$XDG_STATE_HOME/clown/jobs/<channel-id>/<job-id>.jsonl
$XDG_STATE_HOME/clown/jobs/<channel-id>/.ack.json
```

When `XDG_STATE_HOME` is unset it defaults to `$HOME/.local/state`.

Nudge socket (ephemeral):

```
$XDG_RUNTIME_DIR/clown/jobs/<channel-id>.sock
```

When `XDG_RUNTIME_DIR` is unset, implementations MUST fall back to
`$TMPDIR/clown-jobs-<uid>/` (creating it mode `0700`), and `$TMPDIR` itself
defaults to `/tmp`.

The `clown` and `jobs` directories and the per-channel journal directory MUST be
created with mode `0700`. Implementations MUST NOT place a channel under a
world- or group-writable directory.

### 4. Journal Record Schema

Each job has exactly one journal file, `<job-id>.jsonl`, containing one JSON
object per line (JSONL). A job file MUST have a single writer for its lifetime.

A record:

```json
{
  "v": 1,
  "job": "build-3f2ab1c9",
  "session": "clown/sleek-sumac",
  "source": "moxy",
  "type": "succeeded",
  "seq": 2,
  "ts": "2026-06-05T17:04:05.123456789Z",
  "message": "nix build ok",
  "result_ref": "moxy job-read --job build-3f2ab1c9"
}
```

Fields:

- `v` (REQUIRED, integer) — schema version. MUST be `1`.
- `job` (REQUIRED, string) — job id, unique within the channel. MUST match
  `^[A-Za-z0-9._-]{1,128}$`.
- `session` (REQUIRED, string) — the verbatim target session key.
- `source` (REQUIRED, string) — a short label identifying the emitting plugin
  (e.g. `spinclass`, `moxy`). MUST be non-empty.
- `type` (REQUIRED, string) — an event type from the registry in §5.
- `seq` (REQUIRED, integer) — per-job sequence number. The first record (the
  `started` record) MUST have `seq` `0`; each subsequent appended record MUST
  increment `seq` by exactly `1`.
- `ts` (REQUIRED, string) — event time as RFC 3339 with nanosecond precision in
  UTC (`...Z`).
- `message` (OPTIONAL, string) — human-readable detail. MUST NOT contain a
  newline; producers MUST replace any newline with a space.
- `result_ref` (OPTIONAL, string) — an opaque pointer the agent MAY use to fetch
  full results (e.g. a CLI invocation hint or a path). It is data, not a command
  to be auto-executed (§Security Considerations).

Producers MUST write a `started` record (`seq` 0) when a job is created and
exactly one terminal record (§5) when it finishes.

### 5. Event Types and Wake Policy

| `type`        | Terminal | Wakes (v1) | Journaled |
|---------------|----------|------------|-----------|
| `started`     | no       | no         | yes       |
| `progress`    | no       | no         | yes       |
| `succeeded`   | yes      | yes        | yes       |
| `failed`      | yes      | yes        | yes       |
| `cancelled`   | yes      | yes        | yes       |
| `interrupted` | yes      | yes        | yes       |

A **waking** event is one whose `type` has "Wakes" = yes. Exactly the four
terminal types wake in v1.

After a terminal record is written for a job, a producer MUST NOT append further
records to that job.

The type field is an open registry. Types `needs-attention` and `message` are
RESERVED for a future revision in which they wake as non-terminal events (the
spinclass-chat migration described in FDR-0013). A monitor that encounters a
`type` it does not recognize MUST NOT crash and MUST treat the event as
non-waking (journal-only). Consequently a producer MUST NOT rely on a reserved
or unknown type waking an older monitor.

### 6. Nudge Datagram

After durably appending a record (§7), a producer MAY send a single best-effort
datagram to the channel socket (§3) to reduce wake latency. The datagram payload
is a single pipe-delimited line, reusing the clown handshake style (RFC-0002):

```
<wire-version>|<job-id>|<type>
```

- `wire-version` MUST be `1`.
- `job-id` MUST equal the record's `job`.
- `type` MUST equal the record's `type`.

A trailing newline is OPTIONAL and RECOMMENDED. The payload MUST be at most 512
bytes.

The nudge is advisory only. A receiver MUST treat the journal as the source of
truth and MUST re-read it rather than acting on datagram contents directly; the
datagram MAY be lost, duplicated, reordered, or spoofed (within the user's trust
domain). A receiver MAY use `job-id` to read only that job's file.

Producers MUST NOT block on the nudge and MUST ignore send errors (a missing
socket, e.g. when no monitor is running, is the common case on hosts where the
monitor is gated off — see FDR-0013). Correctness MUST NOT depend on the nudge
being delivered.

### 7. Durability and Ordering

For a **terminal** (waking) record, a producer MUST append the record to the
journal file and `fsync` the file (or otherwise guarantee the write is durable)
**before** sending the corresponding nudge. This guarantees the journal is never
behind the socket: a nudge never references an event that is not yet durable.

For non-waking records (`started`, `progress`) the `fsync` is OPTIONAL.

Appends MUST preserve `seq` order; readers rely on `seq` for per-job ordering.

### 8. CLI Conformance Surface

clown MUST provide these subcommands. They are the reference producer and
monitor; the on-disk and on-wire formats above remain the actual contract.

- `clown job start [--target <session-key>] [--label <label>] [--source <s>]`
  — Allocate a job id, create `<job-id>.jsonl`, and append the `started` record
  (`seq` 0). MUST print the job id as a single line to stdout and exit `0`. When
  `--label` is given the id SHOULD be `<sanitized-label>-<8 hex>`; otherwise an
  8+ hex-digit id. When `--target` is omitted the resolved session key (§2) is
  used. When `--source` is omitted it defaults to the value of `CLOWN_JOB_SOURCE`
  or the basename of the producer when discoverable.

- `clown job progress <job-id> [--message <m>]`
  — Append a `progress` record and OPTIONALLY send a nudge. Journal-only; MUST
  NOT wake.

- `clown job done <job-id> --state <succeeded|failed|cancelled|interrupted>
  [--message <m>] [--result-ref <r>]`
  — Append the terminal record, `fsync`, then send the nudge. MUST exit non-zero
  without appending if the job already has a terminal record.

- `clown job read [--job <job-id>] [--since <ts>] [--type <t>]... [--peek]
  [--json]`
  — The pull/observability surface and the fallback delivery path when no
  monitor is running. Without `--job` it MUST return waking events for the
  channel that are new since the caller's read cursor and, unless `--peek` is
  given, advance that cursor past every event scanned. With `--job` it MUST
  return that job's full record stream and MUST NOT advance the cursor.

- `clown job-watch [--once]`
  — The monitor (§9). Resolve the session key (§2), bind the channel socket,
  replay unacked waking events, then block. SIGINT or SIGTERM MUST cause a
  graceful exit with status `0`. The monitor MUST NOT treat stdin EOF as a
  shutdown signal: monitor hosts (Claude Code) spawn monitors with an
  immediately-EOF stdin, so an EOF-triggered exit kills the monitor at session
  start. With `--once` it MUST replay unacked waking events and exit `0`
  without binding the socket or blocking — the deterministic mode used by the
  conformance suite and as a pull-style replay.

When `CLOWN_DISABLE_JOB_WAKEUP` is set to `1`, `clown job-watch` MUST exit `0`
immediately without binding a socket, and the emit subcommands (`start`,
`progress`, `done`) MUST behave as no-ops that still exit `0` (so producers need
no conditional logic).

### 9. Monitor Behavior, Replay, and At-Least-Once Delivery

On start, the monitor MUST, in order:

1. Read `.ack.json` for the channel (treating a missing file as an empty ack
   set).
2. Scan every `<job-id>.jsonl` in the channel directory for waking records whose
   `seq` is greater than the acked sequence for that `job` (or for which the job
   has no ack entry), and emit each such record (step 4), oldest first.
3. Bind the nudge socket (removing a stale socket file at the path first), then
   block. On each received nudge — and additionally on a periodic re-scan timer
   (a safety net against lost nudges) — repeat the scan of step 2 and emit any
   new waking records.

To **emit** a waking record the monitor MUST:

4. Write exactly one line to stdout, with no embedded newline:

   ```
   [clown-job] <source> <job-id> <type>: <message>
   ```

   When `result_ref` is present the monitor MUST append ` · <result_ref>`. When
   `message` is absent the trailing `: ` MUST be omitted.

5. Persist the ack: set the acked sequence for `job` to that record's `seq` in
   `.ack.json`.

`.ack.json` schema:

```json
{ "v": 1, "acked": { "build-3f2ab1c9": 2 } }
```

The channel guarantees each waking event is surfaced **at least once**: because
the ack is persisted after the line is written, a crash between step 4 and step
5 causes the event to be re-emitted on the next monitor start. Consumers
(agents) MUST therefore treat wakeups as idempotent, deduplicating on
(`job`, `type`). Non-waking events have NO delivery guarantee.

A channel SHOULD have at most one active monitor. The monitor MUST unlink its
socket file on graceful shutdown.

### 10. Failure Modes

| Failure | Producer behavior | Monitor behavior |
| --- | --- | --- |
| Nudge socket absent (no monitor) | Ignore send error; journal already durable | n/a — picks up the event on next start via replay |
| Nudge datagram lost/dropped | None (best-effort) | Periodic re-scan surfaces the event |
| `fsync` of terminal record fails | MUST exit non-zero; MUST NOT send nudge | n/a |
| Malformed / partially written journal line | Single-writer invariant prevents torn lines; producer MUST write whole lines | MUST skip the unparseable line without crashing |
| `.ack.json` unreadable/corrupt | n/a | MUST treat as empty (re-emits unacked events; at-least-once preserved) |
| Crash between emit and ack | n/a | Event re-emitted next start (dedupe on consumer) |

## Security Considerations

All channel state lives under per-user directories created mode `0700`
(`$XDG_STATE_HOME/clown/jobs`, the per-channel journal dir, and the runtime
socket dir). The channel is scoped to a single user's trust domain; any process
running as that user can read, write, or spoof journal records and nudge
datagrams. This is the same trust level as any other local tool the user runs;
the facility introduces no cross-user surface and MUST NOT be placed on a
group- or world-accessible path.

Nudge datagrams are unauthenticated. Because the receiver re-reads the journal
and never acts on datagram contents directly (§6), a spoofed datagram can at
most trigger a re-scan; it cannot fabricate a wakeup. Fabricating a wakeup
requires writing a journal record, which requires the same user identity that
could run any producer anyway.

`message` and `result_ref` are emitted into the agent's context. A local process
could therefore inject text the agent reads (a prompt-injection vector at
local-user trust level). `result_ref` is opaque data and MUST NOT be
auto-executed by the monitor; it is surfaced to the agent as text for the agent
to decide upon. Producers SHOULD NOT place secrets in `message`, as the journal
is plaintext on disk and the message is printed to the agent.

## Conformance Testing

Conformance tests for this specification live in `zz-tests_bats/`.

Tests use binary injection via `bats-emo`:

    require_bin CLOWN_BIN clown

### Covered Requirements

| Requirement | Test File | Description |
|-------------|-----------|-------------|
| §2, session key resolution order | `job_wakeup.bats` | `CLOWN_SESSION_ID` > `SPINCLASS_SESSION_ID` > generated |
| §4, record schema & `seq` monotonicity | `job_wakeup.bats` | `start`/`progress`/`done` write conformant JSONL with `seq` 0,1,2 |
| §5, terminal-after-terminal rejected | `job_wakeup.bats` | second `done` on a terminal job exits non-zero |
| §5, only terminal events wake | `job_wakeup.bats` | monitor emits a line for `done` but not for `progress` |
| §7, journal durable before nudge | `job_wakeup.bats` | record present on disk after `done` returns |
| §8, `CLOWN_DISABLE_JOB_WAKEUP` | `job_wakeup.bats` | watch exits 0 without binding; emits are no-ops |
| §9, replay & at-least-once | `job_wakeup.bats` | a monitor started after `done` replays the unacked terminal event once |
| §9, notification line format | `job_wakeup.bats` | emitted line matches `[clown-job] <source> <job> <type>: <message>` |

## Compatibility

This is version 1 of the interface; the `v` field (records) and the
`wire-version` field (datagram) gate future revisions. The event-type registry
(§5) is additively extensible: new types MAY be added, and monitors MUST ignore
unknown types as non-waking, so a newer producer never breaks an older monitor
(it simply does not wake on a type the monitor predates).

The facility is additive and opt-in: a plugin that emits no job events is
unaffected, and `CLOWN_DISABLE_JOB_WAKEUP=1` disables the whole facility with a
single switch (§8). The nudge transport (UDS datagram) is an implementation of
the liveness layer; a future revision MAY define an alternative datagram
transport (e.g. UDP loopback) without changing the journal contract, since
correctness never depends on the nudge.

This RFC reuses the pipe-delimited line style of the clown plugin protocol
handshake (RFC-0002) for the nudge datagram but defines a distinct socket and is
not an MCP transport.

## References

### Normative

- [RFC 2119] Bradner, S., "Key words for use in RFCs to Indicate Requirement
  Levels", BCP 14, RFC 2119, March 1997.
- [RFC-0002] Clown Plugin Protocol (HTTP MCP Server Lifecycle Management).

### Informative

- [FDR-0013] Job-Wakeup Channel (feature-level treatment, rollout, tuning levers,
  spinclass-chat migration).
- clown#110 — Provide a generic long-running-job status socket service for clown
  plugins.
- spinclass#104 — Explore async push-notification of merge/job completion.
- spinclass `internal/chat` — prior-art file-store + monitor pattern this
  specification generalizes.

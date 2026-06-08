---
status: accepted
date: 2026-06-07
---

# Job Output Spool and Status Probe (`clown job status`)

## Abstract

This specification extends the job-wakeup channel (RFC-0009) with a per-job
**output spool** — a producer-written side file carrying the job's live
subprocess output — and a **status probe** (`clown job status`) that reports a
running or finished job's state, elapsed time, last output activity, and a
bounded tail of the spool. Together they make any background job observable
mid-flight through one channel-owned surface, instead of each producer
(spinclass, moxy, …) growing its own job store, log file, and status tool.

## Introduction

RFC-0009 gives producers a durable lifecycle journal and a wake-on-terminal
push, but deliberately captures no subprocess output: a job is invisible
between `started` and its terminal record. Producers that need mid-flight
observability have each built the same thing privately — spinclass streams
hook output to a worktree-local `job.log` whose mtime doubles as a
last-activity signal and serves a 15-line tail from `session-job-status`;
moxy#341 asks for the identical shape for async tool dispatch. The agent-side
cost of this divergence is real: for jobs without a private status tool,
agents resort to probing side effects (re-evaluating nix derivations,
globbing harness temp files) to learn whether a multi-minute job is alive.

This RFC moves that pattern into the channel: the producer tees the job's
output into a spool file that lives next to the job's journal, and clown —
which already knows the job's `started` time and terminal state — serves the
derived status. Any producer that writes the spool conformantly gets
observability for free; any consumer (the originating agent, another session,
a human with a shell) probes every producer's jobs the same way.

Scope: this document specifies the spool file convention, its discovery and
garbage collection, and the `clown job status` / `clown job spool-path` CLI
surface. It does not specify how a producer captures its subprocess output,
nor any MCP-tool façade a producer may layer over the probe (see moxy
FDR-0005 for one such consumer).

## Requirements Language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Specification

### 1. Output Spool File

The output spool for job `<job-id>` on channel `<channel-id>` is

    $XDG_STATE_HOME/clown/jobs/<channel-id>/<job-id>.out

a sibling of the job's journal `<job-id>.jsonl` (RFC-0009 §3).

- The spool is **producer-owned and OPTIONAL**: a job without a spool remains
  fully conformant, and `clown job status` degrades gracefully (§3).
- The producer MUST treat the spool as **append-only** while the job is
  running and MUST NOT write to it after emitting the job's terminal record.
- Content is the job's output as the producer chooses to expose it —
  typically interleaved stdout+stderr in arrival order. The spool is a
  **best-effort liveness surface, not a system of record**: no fsync is
  required, partial trailing lines are permitted, and consumers MUST NOT
  treat spool content as the job's result. Results travel via `result_ref`
  on the terminal record (RFC-0009 §4).
- The spool MUST be created with mode `0600` (the channel directory is
  `0700` per RFC-0009 §3).
- Writers SHOULD bound the spool (for example, stop appending past a size
  cap) rather than mirror unbounded output; the probe only ever reads a
  bounded tail (§3).

Before composing the spool path, an implementation MUST validate `<job-id>`
against the RFC-0009 §4 job-id grammar (`[A-Za-z0-9._-]{1,128}`) and MUST
additionally reject the ids `.` and `..`, which that grammar admits but which
would escape the channel directory.

### 2. Spool Path Discovery (`clown job spool-path`)

    clown job spool-path <job-id> [--target <session-key>]

Resolves the channel (RFC-0009 §2; `--target` selects another session's
channel exactly as in `clown job start`), creates the channel directory if
absent, and prints the absolute spool path for `<job-id>` as a single line.
It MUST NOT create the spool file itself — that is the producer's append.

When the facility is disabled (`CLOWN_DISABLE_JOB_WAKEUP=1`, RFC-0009 §8)
the subcommand MUST exit `0` and print nothing, mirroring the disabled
behavior of `clown job start`: empty stdout on a zero exit is the normal
disabled-channel signature. Producers MUST tolerate an empty path by skipping
the spool (their private fallback, if any, is out of scope here).

A `<job-id>` failing §1 validation is a usage error: exit `2` with a
diagnostic on stderr.

### 3. Status Probe (`clown job status`)

    clown job status <job-id> [--target <session-key>] [--tail <N>] [--json]

A read-only probe, available regardless of `CLOWN_DISABLE_JOB_WAKEUP` (like
`clown job read`, it is a pull, not an emit). It MUST derive, from the job's
journal and spool alone:

| Field | Derivation |
|---|---|
| `state` | type of the journal's terminal record if present, else `running` |
| `started` | `ts` of the `started` record (seq 0) |
| `ended` | `ts` of the terminal record; absent while running |
| `elapsed_sec` | `ended − started` when terminal, else `now − started` |
| `last_activity` | spool mtime when the spool exists; else `ts` of the newest journal record |
| `spool_bytes` | spool size in bytes; `0` / absent when no spool exists |
| `progress` | `message` of the newest `progress` record, if any |
| `tail` | last `N` lines of the spool (default `20`) |

- The tail MUST be read from a bounded trailing window of the spool (an
  implementation-chosen cap, RECOMMENDED 64 KiB) so a probe never scales
  with spool size. A partial first line inside the window MAY be dropped.
- With `--json` the probe MUST emit the fields above as a single JSON
  object on one line, omitting absent optionals. Without `--json` it MUST
  render a one-line human header (`job <id> (<source>): <state>, elapsed
  <…>, last activity <…>`) followed by the tail, if any, under a separator.
- When no journal exists for `<job-id>` on the resolved channel, the probe
  MUST exit `1` with a diagnostic on stderr (unlike `clown job read --job`,
  whose empty stream is a valid answer; a status of nothing is not).
- The probe reports **journal-derived state only**. It MUST NOT guess at
  producer liveness: a job whose producer died without a terminal record
  reports `running` with a stale `last_activity` (the RFC-0009 §10
  producer-death gap is unchanged). Consumers SHOULD treat a long-idle
  `last_activity` as the death signal.

### 4. Garbage Collection

The RFC-0009 §7 sweep MUST reap a job's spool whenever it reaps that job's
journal, and MUST reap orphan spools (a `.out` file whose `.jsonl` sibling is
absent) on the same age policy. Spools impose no new retention knob.

## Security Considerations

- **Spool content is unredacted subprocess output** and may carry secrets
  (tokens echoed by build tools, env dumps in stack traces). The `0600` file
  mode and `0700` channel directory (§1) bound exposure to the owning user,
  matching the journal's posture. Tails surface only through the explicit
  pull probe — they MUST NOT be embedded in notification lines or any other
  push surface, so a wake never leaks output the agent didn't ask for.
- **Path safety**: spool paths embed a caller-supplied job id; §1's grammar
  check plus the `.`/`..` rejection prevents traversal out of the channel
  directory. Implementations MUST apply the same validation in `spool-path`,
  `status`, and the GC sweep.
- **Cross-session reads**: `--target` lets any local process probe any
  session's jobs. This matches RFC-0009's existing trust model — the channel
  trusts everything running as the user — and adds no new boundary.

## Conformance Testing

Conformance tests for this specification live in `zz-tests_bats/`.

Tests use binary injection via `bats-emo`:

    require_bin CLOWN clown

### Covered Requirements

| Requirement | Test File | Description |
|-------------|-----------|-------------|
| §2, spool-path prints channel-dir path; empty + exit 0 when disabled | `job_output_spool.bats` | path shape, disabled-channel signature |
| §1/§2, `.`/`..` and grammar-violating ids rejected with exit 2 | `job_output_spool.bats` | traversal guard |
| §3, status derives state/elapsed from journal records | `job_output_spool.bats` | running vs terminal derivation |
| §3, last_activity from spool mtime; tail bounded to N lines | `job_output_spool.bats` | tail + activity semantics |
| §3, missing journal exits 1 | `job_output_spool.bats` | unknown-job diagnostic |
| §4, sweep reaps spool with journal and orphan spools | `internal/jobwake/status_test.go` | GC coupling (Go: the sweep runs only in the blocking monitor, not via the CLI surface bats drives) |

## Compatibility

Purely additive to RFC-0009: the journal record schema (`v: 1`) is unchanged,
no new record types are introduced, and existing producers and monitors are
unaffected. A producer that never writes a spool loses only the
`last_activity`-from-output and `tail` fields of the probe; a consumer
probing a pre-spool producer's job still gets state, `started`, `elapsed_sec`
and journal-derived `last_activity`. The reserved `needs-attention` event
type (RFC-0009 §5) remains reserved and is unrelated to this surface.

## References

### Normative

- [RFC-0009] `docs/rfcs/0009-job-wakeup-channel.md` — journal schema, channel
  identity, path conventions, disable switch, GC sweep.

### Informative

- [FDR-0013] `docs/features/0013-job-wakeup-channel.md` — feature-level
  treatment of the channel.
- [moxy#341] — async jobs: live status / output tails (the motivating
  consumer).
- [spinclass `internal/job`] — prior art: worktree-local `job.log`, mtime as
  last-activity, 15-line tail in `session-job-status`.
- [clown#117] — full job lifecycle ownership exploration; this RFC is the
  observability slice of that direction.

---
status: experimental
date: 2026-06-06
promotion-criteria: >
  proposed -> experimental: SATISFIED 2026-06-05 — clown ships the `clown job`
  CLI + `clown job-watch` monitor and the bats conformance suite (RFC-0009) is
  green on Linux.
  experimental -> testing: (a) SATISFIED 2026-06-06 — moxy's get-hubbed.ci-watch
  emits real terminal events that wake the agent, proven live end to end
  (real GH run, failure path, notification delivered in-session); (b) pending —
  a second distinct plugin emits on the channel (spinclass chat migration in
  progress).
  testing -> accepted: 7 consecutive days of real async jobs across >=2 plugins
  with zero missed wakeups (every terminal event surfaced) and no tuning-lever
  adjustments in that window; macOS pull-fallback (`clown job-read`) verified.
---

# Job-Wakeup Channel (`clown job`)

## Problem Statement

clown plugins that run work exceeding the MCP client's per-server request
timeout — spinclass's async `merge-this-session` / `check-this-session`, moxy's
long-running tool calls, and future equivalents — cannot *push* a "job done /
failed" signal back to the agent. The agent is forced to poll a status tool (the
documented anti-pattern) or hold a blocking call open and re-subject itself to
the timeout. Every plugin that grows a background-job feature reinvents the same
machinery: a job store, a completion channel, and an ad-hoc way to surface
terminal state. clown should provide that machinery once so any plugin can defer
a long task to the background and have the originating (or a targeted) agent
woken when it completes (clown#110, spinclass#104).

## Interface

The facility has two layers, separated so that *correctness* (never lose a
wakeup) is provided by disk durability and *liveness* (sub-second latency) by a
best-effort socket that is always free to drop:

- **Durable journal** — an append-only on-disk record of each job's lifecycle.
  It is the at-least-once source of truth; a wakeup survives the monitor being
  down, restarting, or a dropped packet, and is replayed on the monitor's next
  start.
- **UDS-datagram nudge** — a lossy "go read the journal now" poke that removes
  poll latency. Losing it costs latency, never correctness.

The push surface is the existing Claude Code **monitor** mechanism: clown
registers a built-in `clown job-watch` monitor for every session (via a
synthesized built-in plugin dir passed with `--plugin-dir`), so plugins do not
each declare it. The monitor prints **one stdout line per *waking* event**,
which the harness delivers to the agent as a notification.

**Wake policy.** Only **terminal** events wake: `succeeded`, `failed`,
`cancelled`, `interrupted`. `started` and `progress` are journal-only — visible
via the pull surface but never a notification, so a chatty job cannot spam the
agent (and cannot trip the harness's "too many events" monitor cutoff). The
event-type field is open: `needs-attention` and `message` are reserved for a
near-term revision that wakes on non-terminal events (the spinclass-chat
migration below).

**Who gets woken.** A job is delivered to a *session key*. By default that is
the session that started the job; clown resolves it as `CLOWN_SESSION_ID`, else
`SPINCLASS_SESSION_ID` (the spinclass `<repo>/<branch>` key under spinclass),
else a generated id, and injects the resolved value into every plugin MCP
server. A producer MAY target a *different* clown session with
`clown job start --target <repo>/<branch>` — this is the "external-agent-wakeup"
case.

**CLI surface (producer + monitor):**

- `clown job start [--target KEY] [--label L] [--source S]` → prints a job id.
- `clown job progress JOB [--message M]` → journal-only liveness.
- `clown job done JOB --state succeeded|failed|cancelled|interrupted [--message M] [--result-ref R]` → durable terminal event + wake.
- `clown job-read [--job JOB] [--since TS] [--type T]... [--peek] [--json]` → pull/observe (and the delivery path on hosts where the monitor is gated off).
- `clown job-watch` → the monitor (clown-registered; not run by hand normally).

Go plugins MAY instead use an optional library that writes the identical
on-disk/on-wire formats; the formats, not the CLI, are the contract (RFC-0009).

**Kill switch.** `CLOWN_DISABLE_JOB_WAKEUP=1` makes `clown job-watch` exit
immediately and the emit subcommands no-op (still exit 0), so the whole facility
can be turned off without touching plugin code.

**macOS / monitor-gated fallback.** Claude Code arms plugin monitors behind a
feature flag that currently resolves false on macOS, so on those hosts the
monitor is a silent no-op. The journal is unaffected, so `clown job-read` is the
documented pull fallback — the same posture spinclass already relies on for
cross-session chat.

## Examples

spinclass async merge — agent backgrounds the merge and is woken on completion
instead of polling `session-job-status`:

    # inside spinclass's async merge runner (pseudo-shell)
    job=$(clown job start --source spinclass --label merge)
    # ... run the long pre-merge hook ...
    clown job done "$job" --state succeeded \
      --message "merge landed on master" \
      --result-ref "spinclass session-job-status"

The agent, idle, receives one notification:

    [clown-job] spinclass merge-9f3c1a2b succeeded: merge landed on master · spinclass session-job-status

A failing run wakes the agent with the failure, not silence:

    clown job done "$job" --state failed --message "pre-merge hook: 2 bats tests red"
    # -> [clown-job] spinclass merge-9f3c1a2b failed: pre-merge hook: 2 bats tests red

moxy backgrounding a long build and waking the same session:

    job=$(clown job start --source moxy --label build)
    clown job progress "$job" --message "evaluating flake"   # journal-only, no wake
    clown job done "$job" --state succeeded --message "nix build ok"

Waking a *different* session (external-agent-wakeup):

    clown job start --target clown/other-branch --source ci --label deploy
    # the agent in session clown/other-branch is woken when that job finishes

Observing progress on demand (pull; never wakes):

    clown job-read --job build-3f2ab1c9 --json

## Limitations

- **Single production consumer so far.** The first consumer — moxy's
  `get-hubbed.ci-watch` (backgrounds a GitHub Actions run, emits a terminal
  `clown job done`, locates clown via the exported `CLOWN_BIN`) — is merged and
  **proven live end to end** (2026-06-06: real run, failure path, failed-job
  enrichment, result-ref, notification delivered into a live session by the
  `clown job-watch` monitor). The channel remains `experimental` rather than
  `testing` until a second distinct plugin emits on it; the spinclass chat
  migration (below) is that second consumer, in progress.
- **Terminal-only wakeups in v1.** A backgrounded job that pauses for input does
  not yet have a waking event; `needs-attention` is reserved but unimplemented.
- **`progress`/`started` are best-effort.** They are journal-only and have no
  delivery guarantee; only terminal events are at-least-once.
- **At-least-once, not exactly-once.** A monitor crash between printing a wakeup
  and persisting its ack re-emits that wakeup on next start; consumers must treat
  wakeups as idempotent (dedupe on job-id + state).
- **Single user trust domain.** Channel state lives under per-user `0700`
  directories; any process running as the user can read or spoof events. No
  cross-user delivery, no authentication on the nudge.
- **One monitor per channel.** Two concurrent monitors on the same session key
  race on the ack file and may each wake.
- **Not a remote/phone push.** "External" means another *local clown session*,
  not a CI runner or a phone notification.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| poll-fallback / re-scan interval | 1s | spinclass-proven; safety net for lost nudges and the macOS pull path | wake latency complaints, or idle-scan CPU shows up |
| journal retention / GC | 7 days | bounds disk; matches session-tombstone-style sweep | journal dir growth becomes material |
| progress-record cap per job | uncapped, journal-only | progress is cheap and never woken | a chatty plugin bloats a single job journal |
| nudge datagram size cap | 512 B | tiny `v|job|type` line; well under any datagram limit | a future need to carry payload in the nudge |
| nudge transport | UDS datagram | fs-scoped perms, no port, tmpfs auto-clean | a non-local emitter ever needs UDP loopback |
| channel-id path encoding | `sha256(key)[:16]` hex | filesystem-safe for keys containing `/` | debugging pain from non-human-readable dirs |

## More Information

- **RFC-0009** — Job-Wakeup Channel: the normative wire/disk contract (journal
  schema, nudge format, socket/path conventions, session-key resolution, replay
  & at-least-once semantics, conformance tests). This FDR is the feature-level
  companion.
- **Rollback.** The facility is additive and opt-in; rollback is a single switch
  (`CLOWN_DISABLE_JOB_WAKEUP=1` / not registering the built-in monitor), never a
  multi-commit revert. spinclass keeps its existing chat monitor until separately
  migrated, so old and new coexist during the dual-architecture period.
- **Forward-looking: spinclass-chat migration.** A chat message addressed to a
  session is exactly a non-terminal *waking* event with addressable targeting.
  Migrating spinclass `internal/chat` onto this channel (via the reserved
  `message`/`needs-attention` types) is the proof the abstraction is right — if
  it cannot express chat, it is the wrong abstraction.
- **Prior art.** spinclass `internal/chat` (file-store + monitor); clown's
  stdio-bridge forward-only heartbeat mode (the *synchronous* progress-keepalive
  cousin, distinct from this async wakeup path).
- clown#110, spinclass#104 — the originating exploration issues.

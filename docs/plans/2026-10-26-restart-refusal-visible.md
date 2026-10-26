# Make a refused restart visible (design note, 2026-10-26)

Labels: **R** read in code, **V** observed, **I** inferred.

## Problem

A restart spinclass already consumed can still be dropped by the frontend, and
today nobody who asked can see that.

- **Cancelled before the stop (old session keeps running).** `frontendRestarter.cancelled`
  (`cmd/clown/restart_relaunch.go`) is reached when the dry-run profile gate
  refuses (`restart_gate.go`), the countdown is cancelled, or a turn started
  during the countdown. R: it counts `clown.restart.cancelled`, writes
  `close-self-run.log` and one line on the frontend's stderr. The request was
  consumed by spinclass's preflight, so nothing retries it.
- **Refused after the stop (old session gone).** R: the relaunched frontend runs
  the `clown resume` profile gate; a refusal there exits 1 with a line on
  stderr. No counter, no record.
- V: stats-me had no `clown.restart.*` series, which is consistent with the
  running clown predating the counters (I, not checked against its build).

## Counters (item 3a)

R: `clown.restart.cancelled`, `.stopping`, `.wake.delivered`, `.wake.failed`
and `clown.relaunch.*` are already emitted by `countMetric`; only the
**after-the-stop refusal** has no counter. Add `clown.restart.refused` for it.
`.delivered`/`.failed` exist as `.wake.delivered`/`.wake.failed` (#329), not
under those names; this note keeps the existing names.

## The record (item 3b), agreed shape with spinclass (light-cedar)

An optional, additive field in the hung-session status file (FDR 0023,
`internal/sessionstatus`), version stays 1, written only by the frontend:

```json
"restart_refusal": {
  "at": "<RFC 3339 UTC, nine fraction digits>",
  "kind": "profile-gate" | "resume" | "countdown" | "turn-started",
  "profile": "<profile name or empty>",
  "pinned_digest": "<empty unless known>",
  "current_digest": "<empty unless known>",
  "text": "<one printable line, bounded>"
}
```

- `profile-gate`: the dry-run refused; the old session keeps running, the file
  stays live and the field stays until the next launch of the principal.
- `resume`: the relaunch's own gate refused after the stop. The new frontend
  writes the file once (frontend pid, no provider) before exiting 1, so the
  holder sees a dead session with the reason.
- `countdown` / `turn-started`: the other two drops of a consumed request,
  because the requester must ask again in those cases too. (Beyond the two
  kinds spinclass proposed; additive, a reader that does not know a kind can
  treat it as "restart dropped".)
- Cleared by construction: a successful launch opens a new Writer, whose first
  write has no field. A later cancelled restart replaces the previous one (a
  new `at`). spinclass dedups on `at` and wakes holders once.
- Digests are filled only where the code already has them; the profile-gate
  error carries text, not digests, so today they are empty (unknown) and
  `text` holds the diff. Not guessed.

## Not in this change

- Waking the holders is spinclass's. clown only writes the field.
- Only the relaunch's profile-gate refusal (`checkLaunchProfile`) writes a
  `resume` record. Other early exits of a relaunched frontend (a flag error, a
  refused agent-scope resume, the closed-session peek) are not recorded; they
  still print to stderr only.
- `restart_refusal` does not change `turn_running` or liveness semantics.

## Tests first

`sessionstatus`: the field round-trips, is omitted when unset, a second set
replaces it, a fresh Writer starts without it. `cmd/clown`: a cancelled
restart (profile gate, countdown, turn started) sets the field with the right
kind and counts; a refusal at the relaunch's gate writes it and counts
`clown.restart.refused`.

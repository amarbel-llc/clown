# Restart cancelled by a turn start: bounded retry (design note)

Status: implemented on master (see the git log for the SHAs), not deployed.
Code: `cmd/clown/restart_defer.go`, the call sites in `restart_relaunch.go` and
`restart_idle.go`. Tests: `cmd/clown/restart_defer_test.go`.

## The problem

After spinclass's `restart-preflight` returns "restart", clown runs a 30 s
countdown (`restart_countdown.go`). A turn that starts during it (the
UserPromptSubmit hook) cancels the restart so the provider is never stopped
mid-turn. That cancel was final: spinclass had already consumed the request,
nothing in clown retried it, and the requester had to ask again. A busy session
(a coordinator's reports every few minutes) could therefore miss its restart.
Observed once on 2026-11-02 (R: the cancel is in the requester's report and
`close-self-run.log`).

## Why not just ask spinclass again

R (spinclass cmd/spinclass/restart_cmd.go at 6a65227, per its owner): a second
`restart-preflight` returns "none", because the first cleared the request and
set the restart stamp. There is no re-arm verb. So the retry cannot be a
re-ask.

## What clown does now

On a turn-start cancel (`RefusalCountdown` whose note ends in
`turnStartedNote`, or `RefusalTurnStarted`), the frontend keeps the verdict it
was given and retries the countdown at the next idle transition, with no
preflight. Bounds: 3 attempts in total, or `restartDeferWindow` (2 minutes,
spinclass's own in-flight window) from the first verdict, whichever comes
first. Only the final cancel is recorded as a refusal. Not retried: an
operator's or viewer's cancel, a profile-gate refusal, a `now` verdict. A
newer queued request replaces a deferred one. Nothing stops the provider
mid-turn: every attempt ends in the same busy checks.

Re-checked on every retry:
- the profile gate (the same dry run as the first attempt);
- spinclass's running-job guard, mirrored from `restartPlan`
  (`runningSpinclassJob`: `<worktree>/.spinclass/job.json` is "running" with a
  live serve pid). A merge/check job started by the turn that cancelled the
  restart makes the retry the final cancel, with the job named, before the
  countdown and again before the stop. Without this the retry would stop the
  provider under the job, which spinclass's guard exists to prevent.

## What the retry does NOT re-check

The retry acts on a verdict from before the cancelling turn, up to 2 minutes
old, and does not re-run spinclass's plan:
- the requester's `restart` right: spinclass checks it only when the request is
  made, so a right revoked inside the window is still honoured;
- that the `[session-entry].restart` hand-off is still configured: clown cannot
  read the trusted sweatfile hierarchy cheaply, and must not guess its trust
  rules;
- anything else `restartPlan` checks that changed since (a session that became
  closed is covered separately by the peek before the relaunch exec).

Also, the 2-minute window starts at `startAttempt`, just after the preflight, so
a retry can begin a little after spinclass's own in-flight window ends. Each
attempt shows a fresh 30 s countdown, and the holders spinclass told
"restarting now" hear nothing until the restart happens or the final cancel is
recorded.

## What a failed retry looks like to the requester

The final cancel is recorded in the status file, and spinclass's `restart-watch`
(5 minutes after the preflight, unconfined sessions only) reports it. The final
attempt only happens at an idle transition, which can be later than that, and a
frontend that exits first only logs "exiting with a deferred restart never
retried" (`noteExit`): in that case the requester hears only the watchdog's
generic "did not come back" message, or nothing for a confined session. So the
requester must still check that the session came back. `restart-watch` is silent
once the relaunched session has stamped its return (R: `runRestartWatch`), so a
retried success produces no false CANCELLED wake.

## The cleaner fix, and where to swap it in

A spinclass `restart-preflight --rearm`: while the restart is in flight and the
session has not returned, restore the request after re-running the whole
restart plan (including the requester's right and the running-job guard) and
supersede the stamp so its watchdog does not report a re-armed restart. It
needs spinclass to change first. The retry call site is the one marked
SWAP POINT in `idleRunner.preflightQueued` (`restart_idle.go`); replacing it
with a re-arming preflight makes `retryBlockedByJob` and the age rules in
`restart_defer.go` unnecessary.

## Not verified

Not run live. Whether a chat/monitor wake counts as a turn (fires
UserPromptSubmit) is claude's behaviour and was not tested; the evidence is
one observed cancel. This note's behaviour follows from unit tests only.

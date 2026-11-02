# Restart cancelled by a turn start: bounded retry (design note)

Status: implemented on branch, not deployed. Code: `cmd/clown/restart_defer.go`,
the call sites in `restart_relaunch.go` and `restart_idle.go`. Tests:
`cmd/clown/restart_defer_test.go`.

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
first. Only the final cancel is recorded as a refusal. Everything else is
unchanged: an operator's or viewer's cancel, a profile-gate refusal and a
`now` verdict are never retried, and every attempt still ends in the same busy
checks, so the provider is never stopped mid-turn. The profile gate is
re-checked on every attempt. A newer queued request replaces a deferred one.

`spinclass restart-watch` is silent once the relaunched session has stamped
its return (R: `runRestartWatch`), so a refusal-free retried success produces
no false CANCELLED wake. A restart whose retries are exhausted records its
refusal well inside the watchdog's 5 minutes.

## The tradeoff to know about

The retry acts on a verdict up to 2 minutes old and does NOT re-check the
requester's `restart` right: spinclass checks it only when the request is made,
not at preflight, and clown has nothing to re-check it against. A right revoked
inside that window is still honoured. The bound keeps the window small.

## Cleaner follow-up

A spinclass `restart-preflight --rearm`: while the restart is in flight and
the session has not returned, restore the request from the stamp after
re-running the checks plus the requester's right, and supersede the stamp so
its watchdog does not report a re-armed restart. It needs spinclass to change
first (that repo's owner has proposed it and has not started). With it the
retry would re-ask instead of acting on a held verdict.

## Not verified

Not run live. Whether a chat/monitor wake counts as a turn (fires
UserPromptSubmit) is claude's behaviour and was not tested; the evidence is
one observed cancel. This note's behaviour follows from unit tests only.

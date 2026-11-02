package main

import (
	"fmt"
	"strings"
	"time"
)

// A restart the countdown cancels because a turn started is not dropped at
// once: the frontend keeps the verdict spinclass already issued and acts on it
// again at the next idle transition (clown's restart-reliability follow-up).
// It cannot re-ask spinclass: the preflight consumed the request, and a second
// `restart-preflight` answers "none". So the retry runs the countdown again
// under the SAME verdict, and the guard is unchanged: it never stops the
// provider while a turn runs (every attempt ends in the same busy checks).
//
// TRADEOFF, read before relying on this: the retry acts on a verdict up to
// restartDeferWindow old, and does not re-check the requester's `restart`
// right (spinclass checks it only when the request is made, not at preflight;
// there is nothing for clown to re-check it against). The bound keeps that
// stale window small: 3 attempts, or restartDeferWindow from the first
// verdict, whichever comes first. The profile gate IS re-checked on every
// attempt. The cleaner fix is a spinclass `restart-preflight --rearm`, which
// would re-check the right and supersede the restart stamp (so its watchdog
// does not report a restart that was re-armed); it needs spinclass to change
// first and is the follow-up if the stale window ever matters. Only the final
// cancel is recorded as a refusal, so spinclass's restart-watch (silent once
// the relaunched session stamped its return) never reports a retried success.

const (
	// maxRestartAttempts is the most countdowns one verdict gets, the first
	// included.
	maxRestartAttempts = 3
	// restartDeferWindow is how long after its verdict a deferred restart may
	// still be retried: spinclass's own in-flight window for the restart
	// (session.RestartInFlightWindow, 2 minutes).
	restartDeferWindow = 2 * time.Minute
)

// turnStartedNote ends the countdown's note whenever it cancelled because a
// turn started, on every path, and never otherwise. It is how the restarter
// tells a turn cancel, which is retried, from an operator's or a viewer's
// cancel, which is final.
const turnStartedNote = "a turn started during the countdown, not restarting"

// deferredRestart is a restart waiting for the next idle transition.
type deferredRestart struct {
	req restartRequest
	v   preflightVerdict
	// first is when the verdict was issued (the first attempt began).
	first time.Time
	// attempts counts the countdowns run for this verdict.
	attempts int
	// kind and why describe the cancel that deferred it, for the refusal if
	// the retry never happens.
	kind, why string
}

func (q *restartQueue) now() time.Time {
	if q != nil && q.clock != nil {
		return q.clock()
	}
	return time.Now()
}

// startAttempt begins one attempt at req under v: the next attempt of a
// deferred restart that takeDeferred handed back, else the first. A nil queue
// has no memory, so its one attempt is final.
func (q *restartQueue) startAttempt(req restartRequest, v preflightVerdict) deferredRestart {
	if q != nil {
		q.mu.Lock()
		d := q.resuming
		q.resuming = nil
		q.mu.Unlock()
		if d != nil && d.req == req && d.v == v {
			d.attempts++
			return *d
		}
	}
	return deferredRestart{req: req, v: v, first: q.now(), attempts: 1}
}

func (q *restartQueue) deferRestart(d deferredRestart) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deferred = &d
}

func (q *restartQueue) hasDeferred() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.deferred != nil
}

// takeDeferred hands back the deferred restart for its retry and remembers it,
// so the restart that follows (startAttempt) counts it as the next attempt.
func (q *restartQueue) takeDeferred() (restartRequest, preflightVerdict, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.deferred == nil {
		return restartRequest{}, preflightVerdict{}, false
	}
	d := q.deferred
	q.deferred, q.resuming = nil, d
	return d.req, d.v, true
}

// turnCancelled handles an attempt a turn start cancelled (kind is a
// sessionstatus.Refusal* value): deferred while the bound allows, else the
// final cancel, which is recorded.
func (fr frontendRestarter) turnCancelled(kind, why string, d deferredRestart) {
	if fr.queue != nil && d.attempts < maxRestartAttempts && fr.queue.now().Sub(d.first) < restartDeferWindow {
		d.kind, d.why = kind, why
		fr.queue.deferRestart(d)
		countMetric(metricRestartDeferred)
		logSelfClose(fr.logPath, "restart (%s) deferred (attempt %d of %d): %s; retrying at the next idle transition", fr.principal, d.attempts, maxRestartAttempts, why)
		return
	}
	if d.attempts > 1 {
		why = fmt.Sprintf("%s (attempt %d of %d)", why, d.attempts, maxRestartAttempts)
	}
	fr.cancelled(kind, why)
}

// retryExpired reports whether a retry of d came too late, and the final
// cancel's text if so.
func (fr frontendRestarter) retryExpired(d deferredRestart) (string, bool) {
	if d.attempts <= 1 || fr.queue.now().Sub(d.first) < restartDeferWindow {
		return "", false
	}
	return fmt.Sprintf("%s; the retry window of %s passed before the turn ended", d.why, restartDeferWindow), true
}

// isTurnCancel reports whether a countdown's note says a turn start cancelled it.
func isTurnCancel(how string) bool { return strings.HasSuffix(how, turnStartedNote) }

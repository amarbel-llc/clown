package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// preflightVerdict is `spinclass restart-preflight`'s one JSON line (spinclass
// FDR 0037): restart (consumed, go ahead), none (nothing to restart), refused
// (an obstacle; spinclass cleared the request and told the holders).
type preflightVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	When    string `json:"when"`
}

// parsePreflightVerdict accepts exactly one JSON line naming a known verdict.
// Anything else is an error, which the caller treats as "do not restart".
func parsePreflightVerdict(out []byte) (preflightVerdict, error) {
	line := strings.TrimSpace(string(out))
	if line == "" {
		return preflightVerdict{}, errors.New("no output")
	}
	if strings.Contains(line, "\n") {
		return preflightVerdict{}, errors.New("more than one line of output")
	}
	var v preflightVerdict
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return preflightVerdict{}, fmt.Errorf("not a verdict: %w", err)
	}
	switch v.Verdict {
	case "restart", "none", "refused":
		return v, nil
	}
	return preflightVerdict{}, fmt.Errorf("unknown verdict %q", v.Verdict)
}

// preflightTimeout bounds `spinclass restart-preflight`; a hung preflight must
// not wedge the idle runner, which also runs close-self-run.
const preflightTimeout = 30 * time.Second

// restartPreflight runs `spinclass restart-preflight` for one session.
type restartPreflight struct {
	bin       string
	principal string
	logPath   string
}

// run asks spinclass whether the pending restart may proceed, with the
// principal in CLOWN_SESSION_ID. A non-zero exit, a timeout or unparsable
// output is an error. Its stderr and the verdict line go to the log.
func (p *restartPreflight) run() (preflightVerdict, error) {
	log, err := openLogForAppend(p.logPath)
	if err != nil {
		return preflightVerdict{}, err
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.bin, "restart-preflight")
	cmd.Env = append(os.Environ(), "CLOWN_SESSION_ID="+p.principal)
	cmd.Stderr = log
	out, err := outputViaFile(cmd)
	fmt.Fprintf(log, "%s restart-preflight %s: %q\n", time.Now().UTC().Format(time.RFC3339), p.principal, out)
	if err != nil {
		return preflightVerdict{}, err
	}
	return parsePreflightVerdict(out)
}

// outputViaFile runs cmd with its stdout in an unlinked temporary file and
// returns what it wrote (64 KiB at most). Unlike a pipe, a file does not hold
// Wait until every process that inherited stdout has closed it, so a
// grandchild the command leaves behind cannot stall the serial idle runner.
func outputViaFile(cmd *exec.Cmd) ([]byte, error) {
	f, err := os.CreateTemp("", "clown-restart-out-*")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_ = os.Remove(f.Name())
	cmd.Stdout = f
	runErr := cmd.Run()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil, err
	}
	return out, runErr
}

// idleRunner is what one idle transition does, in a fixed order: run
// close-self-run, then, only when a restart is queued, take it and ask
// spinclass, restarting only on a "restart" verdict. The queued request is
// taken before the preflight, so whatever the answer (a close that landed
// gives none) it is never asked again.
//
// `clown restart --when now` takes the same path without the idle poke
// (triggerNow), without close-self-run. mu serializes the two preflight and
// restart steps, so no request is preflighted twice and an idle poke's
// preflight during a `now` waits for it; inFlight is held from a `now`
// being accepted, or the idle runner taking a request, until its preflight
// and restart (countdown included) are done, and a `now` arriving meanwhile
// is refused.
type idleRunner struct {
	closer    *selfCloseRunner
	preflight *restartPreflight
	queue     *restartQueue
	restart   func(restartRequest, preflightVerdict)
	logPath   string
	// busy reports a turn running (nil: never). queueIdle uses it to tell a
	// session already idle, which will send no Stop datagram, from one that
	// will.
	busy func() bool

	// closeMu serializes close-self-run runs; mu serializes preflight and
	// restart. onIdle takes closeMu, releases it, then takes mu; the `now`
	// goroutine takes only mu. They are never held together.
	closeMu  sync.Mutex
	mu       sync.Mutex
	inFlight atomic.Bool
	// nows tracks running `now` goroutines (waitNow, for tests).
	nows sync.WaitGroup
	// idles tracks the idle transitions queueIdle started (waitIdle, for
	// tests).
	idles sync.WaitGroup
	// waitingForMu and waitingForCloseMu, when set, are called by onIdle
	// just before it takes mu and closeMu (tests).
	waitingForMu      func()
	waitingForCloseMu func()
}

// triggerNow queues req (replacing a queued `idle`: newest wins) and asks
// spinclass at once, in the background, so the control reply need not wait
// for the preflight. It reports false, queuing nothing, while a preflight or
// restart is already in flight.
func (r *idleRunner) triggerNow(req restartRequest) bool {
	if r.queue == nil || !r.inFlight.CompareAndSwap(false, true) {
		return false
	}
	// Re-checked under inFlight: only its holder stores a plan, so a plan
	// that is not stored now cannot appear before the run below, and the
	// reply the caller gives matches what the run will do.
	if restartUnderway() {
		r.inFlight.Store(false)
		return false
	}
	r.queue.put(req)
	r.nows.Add(1)
	go func() {
		defer r.nows.Done()
		r.mu.Lock()
		defer r.mu.Unlock()
		// Cleared before mu is released, so an idle poke waiting for mu
		// sees it free and handles anything queued meanwhile.
		defer r.inFlight.Store(false)
		if restartUnderway() {
			logSelfClose(r.logPath, "restart now ignored: a restart is underway")
			return
		}
		r.preflightQueued()
	}()
	return true
}

// queueIdle queues req for the next idle transition, and, when no turn is
// running, starts that transition now, in the background. A session already
// idle sends no further Stop datagram until someone prompts it, so without
// this the request would wait for a poke that may never come (clown#327). A
// busy session still waits for its Stop. A request racing a poke is asked
// about once: onIdle takes the request under mu, so the second finds none.
func (r *idleRunner) queueIdle(req restartRequest) {
	r.queue.put(req)
	if r.busy != nil && r.busy() {
		return
	}
	r.idles.Add(1)
	go func() {
		defer r.idles.Done()
		r.onIdle()
	}()
}

// waitIdle waits for the idle transitions queueIdle started so far.
func (r *idleRunner) waitIdle() { r.idles.Wait() }

// restartTriggers are the idle runner's entry points for the control socket.
type restartTriggers struct {
	// now is idleRunner.triggerNow, for `--when now`.
	now func(restartRequest) bool
	// idle is idleRunner.queueIdle, for `--when idle`.
	idle func(restartRequest)
}

// noteExit logs, when the frontend exits, a restart it leaves behind: one in
// flight (spinclass may already have consumed it and holds its restart
// stamp, which refuses a close for a while) or one still queued. A frontend
// exiting for its own restart (the plan is stored) has nothing to say.
func (r *idleRunner) noteExit() {
	if restartUnderway() {
		return
	}
	switch {
	case r.inFlight.Load():
		logSelfClose(r.logPath, "frontend (%s) exiting with a restart check in flight: spinclass may have consumed the request; the requester must ask again", r.preflight.principal)
	case r.queue != nil && r.queue.hasPending():
		logSelfClose(r.logPath, "frontend (%s) exiting with a restart queued and never asked about", r.preflight.principal)
	}
}

// runCloseSelfRun runs close-self-run under closeMu, waiting for one still
// running from an earlier poke, and reports false when a restart became
// underway meanwhile (no close is started then).
func (r *idleRunner) runCloseSelfRun() bool {
	if r.waitingForCloseMu != nil {
		r.waitingForCloseMu()
	}
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if restartUnderway() {
		logSelfClose(r.logPath, "idle poke ignored: a restart is underway")
		return false
	}
	_ = r.closer.run()
	return true
}

// waitNow waits for the `now` runs started so far.
func (r *idleRunner) waitNow() { r.nows.Wait() }

// onIdle runs one idle transition. The close-self-run step holds closeMu,
// the preflight/restart step holds mu; the two are taken one after the
// other, never together (so there is no lock order to invert), and the `now`
// goroutine takes only mu: a long or hung close-self-run delays the next
// poke, which waits for it rather than being dropped, but never a `now`.
func (r *idleRunner) onIdle() {
	// Once a restart has stopped (or is stopping) the provider, an idle poke
	// must not start a close or another restart: the session is between two
	// frontends.
	if restartUnderway() {
		logSelfClose(r.logPath, "idle poke ignored: a restart is underway")
		return
	}
	if !r.runCloseSelfRun() {
		return
	}
	if r.queue == nil {
		return
	}
	if r.waitingForMu != nil {
		r.waitingForMu()
	}
	// sync.Mutex hands the lock to a waiter that has waited over 1 ms
	// (starvation mode), and the idle listener runs one poke at a time, so a
	// `now` waiting here is not starved by a stream of pokes.
	r.mu.Lock()
	defer r.mu.Unlock()
	if restartUnderway() {
		return
	}
	// A `now` accepted while this poke waited for mu runs its own
	// preflight once this poke is done; leave the queue to it.
	if !r.inFlight.CompareAndSwap(false, true) {
		return
	}
	defer r.inFlight.Store(false)
	r.preflightQueued()
}

// preflightQueued takes the queued request, if any, asks spinclass, and
// restarts only on a "restart" verdict. Called with mu held.
func (r *idleRunner) preflightQueued() {
	req, ok := r.queue.take()
	if !ok {
		return
	}
	principal := r.preflight.principal
	v, err := r.preflight.run()
	switch {
	case err != nil:
		logSelfClose(r.logPath, "restart-preflight failed (%s); not restarting: %v", principal, err)
	case v.Verdict != "restart":
		logSelfClose(r.logPath, "restart-preflight verdict %s (%s): %s; not restarting", v.Verdict, principal, sanitizeRestartReason(v.Reason))
	default:
		logSelfClose(r.logPath, "restart-preflight verdict restart (%s): %s; restarting", principal, sanitizeRestartReason(v.Reason))
		r.restart(req, v)
	}
}

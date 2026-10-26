package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

// A restarted session is told it was restarted (clown#329). Spinclass wakes
// the holders of a restarted child ("restarting now", "exited", "is back"),
// but the restarted session itself learned of it only from side effects: a new
// provider pid, a new monitor task id. So the relaunched frontend, once its
// provider is up, writes one wake to its own session on the job-wakeup channel
// (a ringmaster job started and finished at once, so the monitor surfaces it as
// a notification line): when, why and by whom (the reason spinclass's verdict
// carries names the requester), the previous and the new provider pid, and how
// long the restart took. The previous pid and the restart's start travel in
// the relaunch's environment, beside CLOWN_RESTARTED_REASON; nothing is stored.

const (
	// restartedPrevPIDEnv carries the stopped provider's pid to the relaunch.
	restartedPrevPIDEnv = "CLOWN_RESTARTED_PREV_PID"
	// restartedAtEnv carries when the restart began (RFC 3339, UTC).
	restartedAtEnv = "CLOWN_RESTARTED_AT"
)

// providerPID is the running provider's pid (the unit's MainPID when
// confined), 0 until it starts.
var providerPID atomic.Int64

// restartFacts are what the relaunched frontend knows about its own restart.
type restartFacts struct {
	relaunched bool
	reason     string
	prevPID    int
	at         time.Time
}

// restartFactsOfThisLaunch is set by consumeRestartedNotice for a restart's
// relaunch and read by announceRestarted.
var restartFactsOfThisLaunch restartFacts

// consumeRestartFacts reads and unsets the two extra variables. The reason
// was already consumed by the caller. Unparsable values leave the field
// unknown.
func consumeRestartFacts(reason string) restartFacts {
	f := restartFacts{relaunched: true, reason: reason}
	if v, ok := os.LookupEnv(restartedPrevPIDEnv); ok {
		_ = os.Unsetenv(restartedPrevPIDEnv)
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.prevPID = n
		}
	}
	if v, ok := os.LookupEnv(restartedAtEnv); ok {
		_ = os.Unsetenv(restartedAtEnv)
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			f.at = t
		}
	}
	return f
}

// restartWakeMessage is the one line the session reads. The reason came from
// whoever asked spinclass, so it is reduced to one bounded line of printable
// text.
func restartWakeMessage(f restartFacts, newPID int, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "you were restarted at %s: reason: %s", now.UTC().Format(time.RFC3339), sanitizeRestartReason(f.reason))
	switch {
	case f.prevPID > 0 && newPID > 0:
		fmt.Fprintf(&b, "; provider pid %d -> %d", f.prevPID, newPID)
	case newPID > 0:
		fmt.Fprintf(&b, "; new provider pid %d (previous unknown)", newPID)
	}
	if !f.at.IsZero() && now.After(f.at) {
		fmt.Fprintf(&b, "; the restart took %s", now.Sub(f.at).Round(time.Second))
	}
	b.WriteString(" (sent when the new provider process started). Your conversation and principal are the same; your background tasks and monitors were started afresh.")
	return b.String()
}

// wakeWG tracks sends in flight, so the frontend can give one a moment to
// finish before it exits or execs (waitForRestartWake): otherwise a provider
// that dies at once could leave the wake's ringmaster job started and never
// finished.
var wakeWG sync.WaitGroup

// waitForRestartWake waits up to d for a wake being sent.
func waitForRestartWake(d time.Duration) {
	done := make(chan struct{})
	go func() { wakeWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// chainProviderStarted runs the pid-reporting hooks of an agent-scope launch
// (the status file's) and then the restart announcement.
func chainProviderStarted(prev func(int), principal string) func(int) {
	return func(pid int) {
		if prev != nil {
			prev(pid)
		}
		noteProviderStarted(principal, pid)
	}
}

// wakeSender delivers one wake message to the session whose key is
// principal.
type wakeSender func(principal, message string) error

// ringmasterWake delivers a wake the way the job platform defines one: a job
// on the session's own channel, started and finished at once, which the
// session's monitor reports as a notification line. It runs the burned-in
// ringmaster binary with the principal as the session key.
func ringmasterWake(bin string) wakeSender {
	return func(principal, message string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		run := func(args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = append(os.Environ(), "CLOWN_SESSION_ID="+principal)
			out, err := outputViaFile(cmd)
			if err != nil {
				return "", fmt.Errorf("ringmaster %s: %w", args[0], err)
			}
			return strings.TrimSpace(string(out)), nil
		}
		id, err := run("start", "--label", "restarted", "--source", "clown-restart")
		if err != nil {
			return err
		}
		if id == "" || strings.ContainsAny(id, " \t\n") {
			return fmt.Errorf("ringmaster start printed %q, want a job id", id)
		}
		_, err = run("done", id, "--state", "succeeded", "--message", message)
		return err
	}
}

var announceOnce sync.Once

// announceRestarted tells a restart's relaunched session, once, that it was
// restarted. It is called when the provider starts (newPID is its pid) and
// does nothing for any other launch. The send runs in the background so a
// slow ringmaster never holds up the provider; a failure goes to the
// close-self-run log and is not otherwise reported (the session's system
// prompt already carries the restart notice).
func announceRestarted(principal string, newPID int, send wakeSender, logf func(format string, args ...any), now func() time.Time) {
	f := restartFactsOfThisLaunch
	if !f.relaunched || send == nil {
		return
	}
	announceOnce.Do(func() {
		msg := restartWakeMessage(f, newPID, now())
		wakeWG.Add(1)
		go func() {
			defer wakeWG.Done()
			if err := send(principal, msg); err != nil {
				logf("restart wake to %s not delivered: %v", principal, err)
				countMetric(metricRestartWakeFailed)
				return
			}
			countMetric(metricRestartWoke)
		}()
	})
}

// noteProviderStarted records the provider's pid and, for a restart's
// relaunch, announces the restart to the session. It replaces the bare
// status-file call at each place a provider starts.
func noteProviderStarted(principal string, pid int) {
	providerPID.Store(int64(pid))
	var send wakeSender
	if buildcfg.RingmasterPath != "" && !jobWakeupDisabled() {
		send = ringmasterWake(buildcfg.RingmasterPath)
	}
	logPath, _ := userStateWritePath("close-self-run.log")
	announceRestarted(principal, pid, send, func(format string, args ...any) { logSelfClose(logPath, format, args...) }, time.Now)
}

package jugglerrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// ServiceResult is what systemd hands an ExecStopPost command
// (systemd.exec(5) "Environment variables in spawned processes"):
// $SERVICE_RESULT, $EXIT_CODE and $EXIT_STATUS.
type ServiceResult struct {
	Result     string
	ExitCode   string
	ExitStatus string
}

// ServiceResultFromEnv reads the three variables through getenv.
func ServiceResultFromEnv(getenv func(string) string) ServiceResult {
	return ServiceResult{Result: getenv("SERVICE_RESULT"), ExitCode: getenv("EXIT_CODE"), ExitStatus: getenv("EXIT_STATUS")}
}

// HookTerminal is the terminal record the hook writes for a unit whose main
// process left none, per FDR 0019 §6's $SERVICE_RESULT table:
//
//	success                        -> interrupted, crash (the process exited
//	                                  0 without writing its verdict)
//	timeout (RuntimeMaxSec)        -> failed, failed
//	signal                         -> interrupted, killed
//	exit-code, core-dump, oom-kill -> interrupted, crash (also any other or
//	                                  missing result)
//
// The interrupted messages carry the "killed: " / "crash: " prefix
// ExitReasonFor reads back, so the reason is recoverable from the journal.
func HookTerminal(sr ServiceResult) (state, message string) {
	switch sr.Result {
	case "success":
		return StateInterrupted, crashMessagePrefix + "unit exited cleanly without writing a terminal record"
	case "timeout":
		return StateFailed, "wall clock (RuntimeMaxSec) expired"
	case "signal":
		return StateInterrupted, killedMessagePrefix + "main process killed by signal " + orUnknown(sr.ExitStatus)
	}
	return StateInterrupted, fmt.Sprintf("%sunit result %s (exit code %s, status %s)", crashMessagePrefix, orUnknown(sr.Result), orUnknown(sr.ExitCode), orUnknown(sr.ExitStatus))
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// LifecycleDeps are the collaborators of the exit-wake hook, spawn and
// resolve.
type LifecycleDeps struct {
	Ringmaster Ringmaster
	Troupe     Troupe
	Units      UnitLauncher
	// Rooms defaults to ExecRoomProvisioner on the Troupe binary when Troupe
	// is an ExecTroupe.
	Rooms RoomProvisioner
	// MoxyCLI checks a brief's moxyfile against the spawner's before a spawn
	// (FDR 0019 §5); unset, every spawn fails rather than skip the check.
	MoxyCLI MoxyCLI
	// Systemd checks systemd's readiness to start units before a spawn (FDR
	// 0019 Limitations); unset, every spawn fails rather than skip the check.
	Systemd SystemdChecker
	Store   Store
	Now     func() time.Time
	// NewPrincipal (children, fresh run keys) defaults to NewPrincipal.
	NewPrincipal func() string
	// NewRootPrincipal (a run root, from its run key) defaults to
	// NewRootPrincipal.
	NewRootPrincipal func(runKey string) string
	// DoneRetryBackoff paces ExitWake's retry of a `done` call that failed
	// for a reason other than jobwake.ErrAlreadyTerminal (clown#257): the
	// hook is the terminal writer of last resort, so a transient ringmaster
	// failure must not strand a job after a single bad call. Defaults to
	// defaultDoneRetryBackoff; tests set it near-zero to keep the retry
	// loop fast. Bounded by the caller's ctx (the hook's own overall
	// timeout), not a fixed attempt count.
	DoneRetryBackoff time.Duration
}

// defaultDoneRetryBackoff is production's pace for ExitWake's done retry:
// short enough that a handful of attempts still fit inside the hook's own
// 60s budget (cmd/juggler's exitWakeTimeout), long enough not to hammer a
// genuinely down ringmaster.
const defaultDoneRetryBackoff = 2 * time.Second

func (d LifecycleDeps) withDefaults() LifecycleDeps {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.DoneRetryBackoff <= 0 {
		d.DoneRetryBackoff = defaultDoneRetryBackoff
	}
	if d.NewPrincipal == nil {
		d.NewPrincipal = NewPrincipal
	}
	if d.NewRootPrincipal == nil {
		d.NewRootPrincipal = NewRootPrincipal
	}
	if d.MoxyCLI == nil {
		d.MoxyCLI = missingMoxyCLI{}
	}
	if d.Systemd == nil {
		d.Systemd = missingSystemdChecker{}
	}
	if d.Rooms == nil {
		if t, ok := d.Troupe.(ExecTroupe); ok {
			d.Rooms = ExecRoomProvisioner(t)
		} else {
			d.Rooms = missingRoomProvisioner{}
		}
	}
	return d
}

// ExitWakeRequest is one ExecStopPost invocation.
type ExitWakeRequest struct {
	Job string
	// Target is the channel holding the job: the agent's parent principal.
	Target string
	// From is the exiting principal (the unit's CLOWN_SESSION_ID).
	From string
	// LedgerRef is the result_ref the hook attaches when it writes the
	// terminal itself.
	LedgerRef string
	Service   ServiceResult
}

// ExitWakeOutcome reports what the hook did.
type ExitWakeOutcome struct {
	Job           string     `json:"job"`
	State         string     `json:"state"`
	Reason        ExitReason `json:"reason"`
	Message       string     `json:"message"`
	WroteTerminal bool       `json:"wrote_terminal"`
	Woken         []string   `json:"woken"`
	AlreadyWoken  bool       `json:"already_woken"`
}

// ExitWake is the unit's post-stop hook and the sole exit-wake emitter (FDR
// 0019 §6). It writes the terminal record when the main process did not
// (HookTerminal, retried on a transient `done` failure — see
// doneWithRetry), otherwise relays the one already written, then sends ONE
// reason-tagged exit wake to every accepted holder recorded on the job's
// journal. It is idempotent: a terminal record is never written twice, and a
// sent-wake marker under the store keeps a second run from re-waking.
func ExitWake(ctx context.Context, deps LifecycleDeps, req ExitWakeRequest) (ExitWakeOutcome, error) {
	deps = deps.withDefaults()
	out := ExitWakeOutcome{Job: req.Job}
	recs, err := deps.Ringmaster.Records(ctx, req.Target, req.Job)
	if err != nil {
		return out, fmt.Errorf("reading %s: %w", req.Job, err)
	}
	if len(recs) == 0 {
		return out, fmt.Errorf("job %s has no journal on %s's channel", req.Job, req.Target)
	}
	term, terminal := TerminalRecord(recs)
	if !terminal {
		state, message := HookTerminal(req.Service)
		wroteTerm, wrote, derr := doneWithRetry(ctx, deps, req, state, message)
		if derr != nil {
			return out, derr
		}
		term, out.WroteTerminal = wroteTerm, wrote
	}
	out.State, out.Message = term.Type, term.Message
	out.Reason = ExitReasonFor(term.Type, term.Message)

	out.Woken, out.AlreadyWoken, err = sendExitWakes(ctx, deps, exitWakeBatch{
		Job:        req.Job,
		From:       req.From,
		Source:     ExitWakeSource,
		State:      out.State,
		Reason:     out.Reason,
		Message:    out.Message,
		ResultRef:  term.ResultRef,
		Recipients: WakeRecipients(HoldersFromRecords(recs)),
	})
	return out, err
}

// doneWithRetry calls Done until it succeeds, loses a race against another
// writer (jobwake.ErrAlreadyTerminal — relayed, not a failure), or the
// caller's ctx is done (clown#257). The hook is the terminal writer of last
// resort for this job: any other failure (ringmaster transiently
// unreachable, disk full, ...) gets retried, paced by
// deps.DoneRetryBackoff, rather than refused on the first bad call — never
// a refusal before trying at least once, and never more than one terminal
// record written (ringmaster's own first-write-wins settles that).
func doneWithRetry(ctx context.Context, deps LifecycleDeps, req ExitWakeRequest, state, message string) (term JobRecord, wrote bool, err error) {
	for {
		doneErr := deps.Ringmaster.Done(ctx, req.Target, req.Job, DoneRecord{State: state, Message: message, ResultRef: req.LedgerRef})
		if doneErr == nil {
			return JobRecord{Type: state, Message: message, ResultRef: req.LedgerRef}, true, nil
		}
		if errors.Is(doneErr, jobwake.ErrAlreadyTerminal) {
			recs, rerr := deps.Ringmaster.Records(ctx, req.Target, req.Job)
			if rerr != nil {
				return JobRecord{}, false, fmt.Errorf("reading the terminal that won the race for %s: %w", req.Job, rerr)
			}
			if t, ok := TerminalRecord(recs); ok {
				return t, false, nil
			}
			return JobRecord{}, false, fmt.Errorf("ringmaster done %s: already terminal, but no terminal record found on the journal: %w", req.Job, doneErr)
		}
		select {
		case <-ctx.Done():
			return JobRecord{}, false, fmt.Errorf("ringmaster done %s: giving up after repeated failures: %w", req.Job, doneErr)
		case <-time.After(deps.DoneRetryBackoff):
		}
	}
}

type exitWakeBatch struct {
	Job, From, Source, State, Message, ResultRef string
	Reason                                       ExitReason
	Recipients                                   []string
}

// ExitWakeText is one exit wake's message.
func ExitWakeText(job, state string, reason ExitReason, message string) string {
	text := fmt.Sprintf("exit %s %s reason=%s", job, state, reason)
	if message != "" {
		text += " " + message
	}
	return text
}

// sendExitWakes sends one wake per recipient unless the job's sent-wake
// marker exists. The marker is written only after every send succeeded, so a
// partial failure is retried whole (wakes are at-least-once; consumers dedupe
// on (job, type), ringmaster(1) NOTIFICATION LINE). woken is never nil.
func sendExitWakes(ctx context.Context, deps LifecycleDeps, b exitWakeBatch) (woken []string, already bool, err error) {
	woken = []string{}
	marker := deps.Store.wakeMarkerPath(b.Job)
	unlock, err := lock(marker + ".lock")
	if err != nil {
		return woken, false, err
	}
	defer unlock()
	if _, err := os.Stat(marker); err == nil {
		return woken, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return woken, false, err
	}
	text := ExitWakeText(b.Job, b.State, b.Reason, b.Message)
	var errs []error
	for _, holder := range b.Recipients {
		if err := deps.Troupe.SendWake(ctx, Wake{Target: holder, From: b.From, Source: b.Source, Message: text, ResultRef: b.ResultRef}); err != nil {
			errs = append(errs, fmt.Errorf("exit wake to %s: %w", holder, err))
			continue
		}
		woken = append(woken, holder)
	}
	if len(errs) > 0 {
		return woken, false, errors.Join(errs...)
	}
	if err := writeFileAtomic(marker, []byte(deps.Now().UTC().Format(time.RFC3339Nano)+"\n")); err != nil {
		return woken, false, fmt.Errorf("writing exit-wake marker: %w", err)
	}
	return woken, false, nil
}

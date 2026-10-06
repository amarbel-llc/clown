package jugglerrun

import (
	"fmt"
	"strings"

	"code.linenisgreat.com/clown/internal/jugglerloop"
)

// Ringmaster terminal states (FDR 0019 §6) and the derived running state.
const (
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateAborted     = "aborted"
	StateInterrupted = "interrupted"
	StateRunning     = "running"

	// legacyStateCancelled is ringmaster protocol 1's spelling of aborted.
	legacyStateCancelled = "cancelled"
)

// ExitReason is one of FDR 0032 D6's five exit reasons.
type ExitReason string

const (
	ReasonNormal   ExitReason = "normal"
	ReasonFailed   ExitReason = "failed"
	ReasonShutdown ExitReason = "shutdown"
	ReasonCrash    ExitReason = "crash"
	ReasonKilled   ExitReason = "killed"
)

// Process exit codes shared by `juggler run`, `juggler spawn --wait` and the
// other lifecycle verbs (FDR 0019 §1's glue-facing contract).
const (
	ExitSucceeded   = 0
	ExitUsage       = 1
	ExitFailed      = 2
	ExitAborted     = 3
	ExitInterrupted = 4
	ExitWaitTimeout = 5
)

// Message prefixes the exit-wake hook writes on the terminals it authors, so
// the D6 reason is recoverable from the journal alone.
const (
	killedMessagePrefix = "killed: "
	crashMessagePrefix  = "crash: "
)

// ExitCodeForState mirrors a ringmaster state as a process exit code.
func ExitCodeForState(state string) int {
	switch state {
	case StateSucceeded:
		return ExitSucceeded
	case StateFailed:
		return ExitFailed
	case StateAborted, legacyStateCancelled:
		return ExitAborted
	case StateInterrupted:
		return ExitInterrupted
	}
	return ExitUsage
}

// ExitReasonFor maps a terminal record to its D6 reason (FDR 0019 §6):
// succeeded = normal, failed = failed, aborted = shutdown, interrupted =
// killed when the hook recorded a signal death, crash otherwise. Non-terminal
// states have no reason ("").
func ExitReasonFor(state, message string) ExitReason {
	switch state {
	case StateSucceeded:
		return ReasonNormal
	case StateFailed:
		return ReasonFailed
	case StateAborted, legacyStateCancelled:
		return ReasonShutdown
	case StateInterrupted:
		if strings.HasPrefix(message, killedMessagePrefix) {
			return ReasonKilled
		}
		return ReasonCrash
	}
	return ""
}

// AgentVerdict maps a finished loop to its terminal state and message (FDR
// 0019 §4, §6). holderCancelled means the run's ctx was cancelled from
// outside (SIGTERM, a holder's job_cancel): aborted. Otherwise succeeded iff
// the loop ended with end_turn or evaluator_pass AND the final evaluation
// returned true; every other
// ending is failed, with the reason as the message.
func AgentVerdict(res jugglerloop.Result, runErr error, holderCancelled, evaluatorPassed bool, evaluatorErr error) (state, message string) {
	if holderCancelled {
		return StateAborted, "cancelled by a holder"
	}
	switch res.End {
	case jugglerloop.EndTurn, jugglerloop.EndEvaluatorPass:
		// evaluator_pass (brief evaluator.stop_on_pass) is judged by the same
		// final evaluation as end_turn, not special-cased.
		if evaluatorPassed {
			return StateSucceeded, "evaluator passed"
		}
		if evaluatorErr != nil {
			return StateFailed, "evaluator false: " + evaluatorErr.Error()
		}
		return StateFailed, "evaluator false"
	case jugglerloop.EndCannotComplete:
		reason := ""
		if res.Ledger.CannotComplete != nil {
			reason = res.Ledger.CannotComplete.Reason
		}
		return StateFailed, fmt.Sprintf("cannot_complete: %q", reason)
	case jugglerloop.EndStepCap:
		return StateFailed, fmt.Sprintf("step_cap: step cap reached after %d steps", res.Ledger.Steps)
	case jugglerloop.EndTimeout:
		return StateFailed, "timeout: the brief's wall clock expired"
	case jugglerloop.EndToolError:
		detail := "a tool transport failed"
		if n := len(res.Ledger.Calls); n > 0 && res.Ledger.Calls[n-1].Error != "" {
			detail = res.Ledger.Calls[n-1].Error
		}
		return StateFailed, "tool_error: " + detail
	case jugglerloop.EndMaxTokens:
		return StateFailed, "reply truncated by max_tokens"
	case jugglerloop.EndModelError:
		detail := "the model endpoint failed"
		if runErr != nil {
			detail = runErr.Error()
		}
		return StateFailed, "model_error: " + detail
	}
	return StateFailed, fmt.Sprintf("unknown loop end %q", res.End)
}

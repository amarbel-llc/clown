package jugglerrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// SystemdReadiness is one `systemctl is-system-running` result: State is the
// manager's reported state (its stdout, trimmed), whatever its exit code.
// Detail is set only when State is empty because the command's own exit
// (not its state) is the only information available — e.g. D-Bus or
// polkit down, or no user bus under --user — and carries systemctl's own
// explanation (its exit code and stderr) so that case is never reported as
// if systemd had genuinely answered "unknown".
type SystemdReadiness struct {
	State  string
	Detail string
}

// SystemdChecker reports systemd's own readiness to start units
// (`systemctl is-system-running`, systemctl(1)): the spawn precondition FDR
// 0019 Limitations describes ("juggler spawn SHOULD check systemctl
// is-system-running as a precondition").
type SystemdChecker interface {
	// IsSystemRunning reports the manager's state whatever its exit code:
	// systemctl exits non-zero for every state but "running", so a
	// non-"running" state is not itself an error. A returned error means
	// the check itself could not be run at all (no binary, killed by ctx).
	IsSystemRunning(ctx context.Context) (SystemdReadiness, error)
}

// ExecSystemdChecker shells `systemctl is-system-running` (or `--user`
// under UserManager, matching ExecSystemdRun's launch).
type ExecSystemdChecker struct {
	Bin         string
	UserManager bool
}

func (c ExecSystemdChecker) IsSystemRunning(ctx context.Context) (SystemdReadiness, error) {
	argv := []string{c.Bin}
	if c.UserManager {
		argv = append(argv, "--user")
	}
	argv = append(argv, "is-system-running")
	out, err := runCommand(ctx, Command{Argv: argv, Env: StripPrincipalEnv(os.Environ())})
	state := strings.TrimSpace(string(out))
	if err == nil {
		return SystemdReadiness{State: state}, nil
	}
	var ce *CommandError
	if errors.As(err, &ce) && ce.ExitCode >= 0 {
		// systemctl exited and reported its state on stdout (possibly
		// empty); its own exit code is non-zero for everything but
		// "running" and is not itself a failure of the check. When no
		// state came back at all, the exit is the only information this
		// check has (e.g. D-Bus/polkit down) — keep it as Detail rather
		// than discarding it, so the refusal is never indistinguishable
		// from systemd genuinely reporting "unknown".
		r := SystemdReadiness{State: state}
		if state == "" {
			if stderr := firstLine(ce.Stderr); stderr != "" {
				r.Detail = fmt.Sprintf("systemctl exit %d: %s", ce.ExitCode, stderr)
			} else {
				r.Detail = fmt.Sprintf("systemctl exit %d (no stderr)", ce.ExitCode)
			}
		}
		return r, nil
	}
	return SystemdReadiness{}, fmt.Errorf("the systemd readiness check needs systemctl, and %q could not be run (set --systemctl or $%s): %w", c.Bin, SystemctlBinEnv, err)
}

// maxDetailStderr bounds the stderr line SystemdReadiness.Detail carries, so
// a verbose (or hostile) systemctl can't bloat a refusal message or ledger
// entry.
const maxDetailStderr = 256

// firstLine returns s's first line, trimmed and capped at maxDetailStderr
// bytes.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxDetailStderr {
		s = s[:maxDetailStderr]
	}
	return s
}

// missingSystemdChecker stands in when LifecycleDeps names no SystemdChecker:
// the precondition never silently passes.
type missingSystemdChecker struct{}

var errNoSystemdChecker = errors.New("no systemctl binary is configured for the systemd readiness check")

func (missingSystemdChecker) IsSystemRunning(context.Context) (SystemdReadiness, error) {
	return SystemdReadiness{}, errNoSystemdChecker
}

// AcceptableSystemdStates are the systemctl is-system-running values under
// which a spawn may proceed: `running`, and `degraded` (an unrelated unit
// failed; the manager still starts units). Anything else — starting,
// initializing, maintenance, stopping, offline, unknown, or a non-zero exit
// with no recognized state (normalized to "unknown") — refuses the spawn
// (FDR 0019 Limitations).
var AcceptableSystemdStates = map[string]bool{
	"running":  true,
	"degraded": true,
}

// ErrSystemdNotReady is CheckSystemdReady's refusal: State is the manager's
// reported is-system-running value, or "unknown" when the check could not
// read a recognized one. Detail, when set, is systemctl's own explanation
// for why no state came back at all (its exit code and stderr) — carried
// through rather than discarded, so a D-Bus/polkit-down host is never
// reported identically to systemd genuinely answering "unknown".
type ErrSystemdNotReady struct {
	State  string
	Detail string
}

func (e *ErrSystemdNotReady) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("systemd is not ready to start units: is-system-running reported no state (%s)", e.Detail)
	}
	return fmt.Sprintf("systemd is not ready to start units: is-system-running reported %q", e.State)
}

// CheckSystemdReady is the spawn precondition (FDR 0019 Limitations): it
// MUST run before any side effect of a spawn — before the moxyfile
// narrowing check and before any principal is minted, so a degraded systemd
// produces a legible, synchronous failure before a `systemd-run` error
// would. Accepts AcceptableSystemdStates; refuses anything else, including
// a check that could not read a recognized state. A checker that could not
// be run at all (missing binary, killed) is a distinct error, not a
// refusal.
func CheckSystemdReady(ctx context.Context, checker SystemdChecker) error {
	r, err := checker.IsSystemRunning(ctx)
	if err != nil {
		return fmt.Errorf("checking systemd is ready to start units: %w", err)
	}
	state := r.State
	if state == "" {
		state = "unknown"
	}
	if AcceptableSystemdStates[state] {
		return nil
	}
	return &ErrSystemdNotReady{State: state, Detail: r.Detail}
}

// SystemdRefusalEntry is the run ledger's record of a spawn refused by
// CheckSystemdReady (FDR 0019 Limitations), auditable without a child
// record, the same way a widening brief is (see NarrowsRefusalEntry): the
// manager's reported state, and its own detail when no state could be read.
func SystemdRefusalEntry(state, detail string) RunLedgerEntry {
	return RunLedgerEntry{
		Tool: "systemd", Kind: "refusal", OK: false, URIs: []string{},
		Reason: "systemd is not ready to start units", State: state, Detail: detail,
	}
}

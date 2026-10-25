package agentscope

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"code.linenisgreat.com/clown/internal/ptysuspend"
)

// Tools are the systemd client binaries. Empty fields resolve from PATH.
type Tools struct {
	SystemdRun string
	Systemctl  string

	// show and procRoot replace systemctl and /proc in tests.
	show     func(unit string) (Status, error)
	procRoot string
}

func (t Tools) systemdRun() string { return orName(t.SystemdRun, "systemd-run") }
func (t Tools) systemctl() string  { return orName(t.Systemctl, "systemctl") }

func orName(p, name string) string {
	if p != "" {
		return p
	}
	return name
}

// ManagerEnvKeys lists the keys the user manager injects into every unit.
func (t Tools) ManagerEnvKeys() ([]string, error) {
	out, err := exec.Command(t.systemctl(), "--user", "show-environment").Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl --user show-environment: %w", err)
	}
	var keys []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, _, ok := strings.Cut(sc.Text(), "="); ok && k != "" {
			keys = append(keys, k)
		}
	}
	return keys, sc.Err()
}

// Status is the slice of `systemctl --user show` the result needs.
type Status struct {
	ActiveState  string
	SubState     string
	ControlGroup string
	InvocationID string
	MainPID      int
	// Result is systemd's verdict on a stopped unit: "exit-code" or
	// "signal" when a process in it ran and ended; anything else (e.g.
	// "exec", "resources", "protocol") when it never got that far.
	Result string
	// ShimSeen is set by waitActive once it saw the unit's settled main
	// process (the shim) running.
	ShimSeen bool
}

// ranAndEnded reports whether a failed unit's Result says a process in it
// ran, as opposed to the unit failing to start.
func (s Status) ranAndEnded() bool {
	return s.Result == "exit-code" || s.Result == "signal" || s.Result == "core-dump"
}

// Show reads a unit's status.
func (t Tools) Show(unit string) (Status, error) {
	out, err := exec.Command(t.systemctl(), "--user", "show",
		"-p", "ActiveState", "-p", "SubState", "-p", "ControlGroup", "-p", "InvocationID", "-p", "MainPID", "-p", "Result",
		unit).Output()
	if err != nil {
		return Status{}, fmt.Errorf("systemctl --user show %s: %w", unit, err)
	}
	var st Status
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "ActiveState":
			st.ActiveState = v
		case "SubState":
			st.SubState = v
		case "ControlGroup":
			st.ControlGroup = v
		case "InvocationID":
			st.InvocationID = v
		case "MainPID":
			st.MainPID, _ = strconv.Atoi(v)
		case "Result":
			st.Result = v
		}
	}
	return st, sc.Err()
}

// waitActive polls until the unit is active with its lasting main process
// (settledMain) and that process has exec'd the provider (providerStarted),
// the context ends, or the unit fails. Until the exec, the main process is
// the shim, which may still refuse (its relays or in-unit servers failing),
// so "active" alone does not mean the launch succeeded.
func (t Tools) waitActive(ctx context.Context, unit string) (Status, error) {
	show := t.show
	if show == nil {
		show = t.Show
	}
	seen := false
	for {
		st, err := show(unit)
		if err == nil && st.ActiveState == "active" && st.MainPID > 0 && st.ControlGroup != "" && t.settledMain(st.MainPID, unit) {
			seen = true
			if t.providerStarted(st.MainPID) {
				st.ShimSeen = true
				return st, nil
			}
		}
		st.ShimSeen = seen
		if err == nil && st.ActiveState == "failed" {
			if !st.ranAndEnded() {
				return st, fmt.Errorf("unit %s failed to start (result %s)", unit, st.Result)
			}
			// A provider that ran and exited nonzero between two polls
			// leaves the unit failed too; Run tells it apart from a shim
			// refusal by the exit code.
			return st, fmt.Errorf("unit %s ended (%s): %w", unit, st.SubState, errUnitEnded)
		}
		select {
		case <-ctx.Done():
			return st, fmt.Errorf("unit %s not active: %w", unit, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// settledMain reports whether pid is the process that stays the unit's main
// process: alive in the unit's cgroup and the init of the unit's own pid
// namespace (PrivatePIDs=yes). When the unit first turns active, systemd still
// names the intermediate process that unshared the namespace; it exits once
// its child, pid 1 inside, runs, and systemd then names that child (observed,
// clown#273: `just explore explore-agent-scope-mainpid`). pid 1 inside is the
// exec shim, which execs the provider, so the pid holds for the unit's life.
func (t Tools) settledMain(pid int, unit string) bool {
	root := orName(t.procRoot, "/proc")
	if checkUnitCgroup(pid, unit, func(pid int) (string, error) { return procCgroupAt(root, pid) }) != nil {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "NSpid:"); ok {
			ids := strings.Fields(rest)
			return len(ids) > 1 && ids[len(ids)-1] == "1"
		}
	}
	return false
}

// providerStarted reports whether pid, the unit's main process, has started
// the provider. As the pid namespace's init (the usual case) the shim stays
// pid 1, starts the provider as its child, and then renames itself InitComm.
// A shim that is not pid 1 execs the provider in place instead, after which
// its argv is no longer the shim's (`<clown> agent-exec ...`).
func (t Tools) providerStarted(pid int) bool {
	dir := filepath.Join(orName(t.procRoot, "/proc"), strconv.Itoa(pid))
	if comm, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil && strings.TrimSpace(string(comm)) == InitComm {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil || len(raw) == 0 {
		return false
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	return len(args) < 2 || args[1] != ShimVerb
}

// InitComm is the process name the shim, as init, takes once the provider
// runs (at most 15 bytes, the kernel's comm limit).
const InitComm = "clown-init"

// ShimVerb is the clown subcommand the unit's main process runs until it
// execs the provider.
const ShimVerb = "agent-exec"

// The shim's own exit codes, for a refusal before the provider starts: bad
// usage, its relays or in-unit servers failing (including a profile's
// unconfirmed exclusions), the terminal, or the exec itself; and a provider
// not found. A provider that itself exits with one of these before clown
// sees it start is reported as a shim refusal.
const (
	ShimExitUsage    = 2
	ShimExitRefused  = 126
	ShimExitNotFound = 127
)

// ShimRefused reports whether code is one of the shim's own refusal codes.
func ShimRefused(code int) bool {
	return code == ShimExitUsage || code == ShimExitRefused || code == ShimExitNotFound
}

// startupTimeout bounds how long Run waits, after starting the unit, for the
// provider to be exec'd: unit activation plus the shim's relays and in-unit
// servers (moxy, just-us). Start value 2m; raise it if a launch on a cold
// nix store is reported as "provider did not start" while the unit was still
// starting its servers.
const startupTimeout = 2 * time.Minute

// Run starts the unit and waits for it to exit, returning the provider's exit
// code. The terminal is never handed to the unit: when stdin is a terminal,
// clown relays through a fresh pty whose slave the in-unit shim makes its
// controlling tty. Once the provider has started in the unit, the result is
// written to the spec's result_path; it is rewritten with the exit code when
// the unit exits, and with the error when the launch fails, including a unit
// that exits before its provider started.
func Run(l Launch, tools Tools, logger *slog.Logger) (int, error) {
	args, err := l.SystemdRunArgs()
	if err != nil {
		return 1, l.fail(err)
	}
	argv := append([]string{tools.systemdRun()}, args...)
	if logger != nil {
		logger.Info("agent scope: starting unit", "unit", l.Unit, "argv", argv)
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	if !interactive && (term.IsTerminal(int(os.Stdout.Fd())) || term.IsTerminal(int(os.Stderr.Fd()))) {
		return 1, l.fail(errors.New("stdout or stderr is a terminal but stdin is not; refusing to hand the operator's terminal to the agent scope"))
	}

	// The report goroutine waits for the provider to start and writes the
	// success result; the exit cancels it, and the outcome decides what the
	// result says last.
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	outcome := make(chan startOutcome, 1)
	var reporting atomic.Bool
	report := func(pty string, pid int) {
		reporting.Store(true)
		if l.OnStarted != nil {
			l.OnStarted(pid)
		}
		go func() {
			st, err := tools.waitActive(ctx, l.Unit)
			res := l.baseResult()
			res.Cgroup, res.InvocationID, res.Pty, res.MainPID = st.ControlGroup, st.InvocationID, pty, st.MainPID
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errUnitEnded) {
				// The unit failed, or the provider did not start in time:
				// say so now, not only when the unit exits.
				_ = l.fail(err)
			}
			if err == nil {
				l.writeResult(res, logger)
				if logger != nil {
					logger.Info("agent scope: provider started", "unit", l.Unit, "cgroup", st.ControlGroup, "main_pid", st.MainPID, "pty", pty)
				}
			}
			outcome <- startOutcome{res: res, err: err, shimSeen: st.ShimSeen}
		}()
	}
	defer tools.stopIfActive(l.Unit)

	var code int
	if interactive {
		code, err = ptysuspend.Run(argv, os.Stdin, ptysuspend.Options{
			DetachSlave: true,
			NoEscape:    true,
			OnStart:     func(slave string, pid int) { report(slave, pid) },
		})
	} else {
		code, err = runPiped(argv, report)
	}
	cancel()
	if !reporting.Load() {
		if err == nil {
			err = fmt.Errorf("systemd-run exited (code %d) before the unit started", code)
		}
		return code, l.fail(err)
	}
	o := <-outcome
	switch {
	case o.err == nil:
	case !errors.Is(o.err, context.Canceled) && !errors.Is(o.err, errUnitEnded):
		// Already recorded by the report goroutine; the exit does not undo it.
		if err == nil {
			err = o.err
		}
		return code, l.fail(o.err)
	case ShimRefused(code):
		why := fmt.Errorf("the unit's exec shim refused before the provider started (exit %d; its stderr says why)", code)
		if err == nil {
			err = why
		}
		return code, l.fail(why)
	case code != 0 && !o.shimSeen && !errors.Is(o.err, errUnitEnded):
		// No evidence anything ran: systemd neither reported a process
		// exit nor did clown see the shim. A unit that failed to start, or
		// systemd-run refusing, ends here too.
		why := fmt.Errorf("the unit ended (exit %d) before clown saw it running: either it failed to start or its provider exited at once", code)
		if err == nil {
			err = why
		}
		return code, l.fail(why)
	default:
		// The unit exited before clown saw the provider start: a provider
		// that ran briefly, observed by its exit alone.
		o.res = l.baseResult()
	}
	exit := code
	o.res.ExitCode = &exit
	if err != nil {
		o.res.Error = err.Error()
	}
	l.writeResult(o.res, logger)
	return code, err
}

// errUnitEnded is a unit that stopped before clown saw its provider start.
var errUnitEnded = errors.New("the unit ended before clown saw its provider start")

type startOutcome struct {
	res      Result
	err      error
	shimSeen bool
}

func (l Launch) writeResult(r Result, logger *slog.Logger) {
	if l.Spec.ResultPath == "" {
		return
	}
	if err := WriteResult(l.Spec.ResultPath, r); err != nil && logger != nil {
		logger.Error("agent scope: writing result", "err", err)
	}
}

func runPiped(argv []string, report func(pty string, pid int)) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	go func() {
		for s := range sigCh {
			_ = cmd.Process.Signal(s)
		}
	}()
	report("", cmd.Process.Pid)
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// stopIfActive makes sure the unit does not outlive the systemd-run client
// (an interrupted client leaves --wait units running).
func (t Tools) stopIfActive(unit string) {
	if st, err := t.Show(unit); err == nil && (st.ActiveState == "active" || st.ActiveState == "activating") {
		_ = exec.Command(t.systemctl(), "--user", "stop", unit).Run()
	}
}

// Fail records err in the result file (when the spec names one) and returns
// it: for a launch refused after its spec loaded, before or during Run.
func (l Launch) Fail(err error) error { return l.fail(err) }

func (l Launch) fail(err error) error {
	if l.Spec.ResultPath != "" {
		_ = WriteResult(l.Spec.ResultPath, Result{Unit: l.Unit, UID: os.Getuid(), Error: err.Error()})
	}
	return err
}

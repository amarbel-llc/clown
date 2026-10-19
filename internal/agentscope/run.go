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
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"code.linenisgreat.com/clown/internal/ptysuspend"
)

// Tools are the systemd client binaries. Empty fields resolve from PATH.
type Tools struct {
	SystemdRun string
	Systemctl  string
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
}

// Show reads a unit's status.
func (t Tools) Show(unit string) (Status, error) {
	out, err := exec.Command(t.systemctl(), "--user", "show",
		"-p", "ActiveState", "-p", "SubState", "-p", "ControlGroup", "-p", "InvocationID", "-p", "MainPID",
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
		}
	}
	return st, sc.Err()
}

// waitActive polls until the unit is active with a main pid, the context ends,
// or the unit fails.
func (t Tools) waitActive(ctx context.Context, unit string) (Status, error) {
	for {
		st, err := t.Show(unit)
		if err == nil && st.ActiveState == "active" && st.MainPID > 0 && st.ControlGroup != "" {
			return st, nil
		}
		if err == nil && st.ActiveState == "failed" {
			return st, fmt.Errorf("unit %s failed to start (%s)", unit, st.SubState)
		}
		select {
		case <-ctx.Done():
			return st, fmt.Errorf("unit %s not active: %w", unit, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Run starts the unit and waits for it to exit, returning the provider's exit
// code. The terminal is never handed to the unit: when stdin is a terminal,
// clown relays through a fresh pty whose slave the in-unit shim makes its
// controlling tty. Once the unit is active, the result is written to the
// spec's result_path; on failure the result carries the error.
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

	reported := make(chan struct{})
	report := func(pty string) {
		go func() {
			defer close(reported)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			st, err := tools.waitActive(ctx, l.Unit)
			if err != nil {
				_ = l.fail(err)
				return
			}
			res := Result{Unit: l.Unit, Principal: l.Principal, Cgroup: st.ControlGroup, InvocationID: st.InvocationID, UID: os.Getuid(), Pty: pty, MainPID: st.MainPID}
			if l.Spec.ResultPath != "" {
				if err := WriteResult(l.Spec.ResultPath, res); err != nil && logger != nil {
					logger.Error("agent scope: writing result", "err", err)
				}
			}
			if logger != nil {
				logger.Info("agent scope: unit active", "unit", l.Unit, "cgroup", st.ControlGroup, "main_pid", st.MainPID, "pty", pty)
			}
		}()
	}
	defer tools.stopIfActive(l.Unit)

	var code int
	if interactive {
		code, err = ptysuspend.Run(argv, os.Stdin, ptysuspend.Options{
			DetachSlave: true,
			NoEscape:    true,
			OnStart:     func(slave string, _ int) { report(slave) },
		})
	} else {
		code, err = runPiped(argv, report)
	}
	select {
	case <-reported:
	case <-time.After(time.Second):
	}
	return code, err
}

func runPiped(argv []string, report func(pty string)) (int, error) {
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
	report("")
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

// fail records err in the result file (when the spec names one) and returns it.
func (l Launch) fail(err error) error {
	if l.Spec.ResultPath != "" {
		_ = WriteResult(l.Spec.ResultPath, Result{Unit: l.Unit, UID: os.Getuid(), Error: err.Error()})
	}
	return err
}

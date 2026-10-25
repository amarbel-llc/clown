package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// restartCountdownSeconds is the operator's ruling (spinclass FDR 0037
// decision 9): an attached session gets 30 seconds before it restarts.
const restartCountdownSeconds = 30

// restartCountdown decides, after a "restart" verdict, whether to restart now.
// A posh-wrapped session whose posh has the app modal shows it (posh's daemon
// owns the clock; exit 0 proceeds, 10 is a viewer's cancel). Any other
// attached terminal gets an inline notice and the same wait, with no cancel. A
// detached session restarts at once.
type restartCountdown struct {
	// poshSession is POSH_SESSION, set inside a posh session.
	poshSession string
	lookPath    func(string) (string, error)
	// run runs argv and returns its stdout and exit code; err is a failure to
	// run at all.
	run         func(argv []string) ([]byte, int, error)
	interactive bool
	notify      io.Writer
	sleep       func(time.Duration)
}

func newRestartCountdown() restartCountdown {
	return restartCountdown{
		poshSession: os.Getenv("POSH_SESSION"),
		lookPath:    exec.LookPath,
		run:         runForCountdown,
		interactive: isInteractiveTerminal(),
		notify:      os.Stderr,
		sleep:       time.Sleep,
	}
}

// runForCountdown runs posh, bounded past the modal's own deadline.
func runForCountdown(argv []string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), (restartCountdownSeconds+15)*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	return out.Bytes(), 0, nil
}

// proceed reports whether to restart, and how the countdown went (for the log).
func (c restartCountdown) proceed(reason string) (bool, string) {
	how := ""
	if c.poshSession != "" {
		ok, decided, note := c.poshModal(reason)
		if decided {
			return ok, note
		}
		how = note + "; "
	}
	if !c.interactive {
		return true, how + "detached: no countdown"
	}
	fmt.Fprintf(c.notify, "\r\nclown: restart pending (%s); restarting in %ds\r\n", reason, restartCountdownSeconds)
	c.sleep(restartCountdownSeconds * time.Second)
	return true, how + "inline countdown elapsed"
}

// poshModal raises posh's restart modal when this posh has one. decided is
// false when it could not (no posh, no modal, busy or invalid), so the caller
// falls back.
func (c restartCountdown) poshModal(reason string) (proceed, decided bool, note string) {
	posh, err := c.lookPath("posh")
	if err != nil {
		return false, false, "posh is not on PATH"
	}
	out, code, err := c.run([]string{posh, "modal", "--probe", c.poshSession})
	if err != nil || code != 0 || !poshProbeHasModal(out) {
		return false, false, "posh has no app modal"
	}
	_, code, err = c.run([]string{
		posh, "modal", c.poshSession,
		"--title", "Restarting agent",
		"--body", "restart requested: " + reason,
		"--timeout", strconv.Itoa(restartCountdownSeconds),
		"--id", "clown-restart-" + strconv.Itoa(os.Getpid()),
	})
	switch {
	case err != nil:
		return false, false, fmt.Sprintf("posh modal failed: %v", err)
	case code == 0:
		return true, true, "posh modal: proceed"
	case code == 10:
		return false, true, "posh modal: cancelled by a viewer"
	}
	return false, false, fmt.Sprintf("posh modal exited %d", code)
}

func poshProbeHasModal(out []byte) bool {
	var p struct {
		Modal string `json:"modal"`
	}
	return json.Unmarshal(bytes.TrimSpace(out), &p) == nil && p.Modal == "ok"
}

package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/amarbel-llc/clown/internal/ptysuspend"
)

// runPtySuspend is the hidden `clown pty-suspend -- <cmd> [args...]` POC entry
// (FDR-0017 ctrl-z recon). It wraps an arbitrary command in the ptysuspend
// proxy so ^Z escapes to a shell even when the command runs a raw-mode TUI.
// Generic on purpose — claude is only one downstream provider.
//
// When stdout/stdin are not an interactive terminal there is nothing to proxy,
// so it runs the command with inherited stdio. Not wired into the default
// provider path yet; see the plan's follow-ups.
func runPtySuspend(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown pty-suspend: usage: clown pty-suspend -- <cmd> [args...]")
		return 2
	}

	if !isInteractiveTerminal() {
		c := exec.Command(args[0], args[1:]...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			fmt.Fprintf(os.Stderr, "clown pty-suspend: %v\n", err)
			return 1
		}
		return 0
	}

	// POC subcommand: the escape key (^Z) drops to the user's $SHELL in cwd.
	code, err := ptysuspend.Run(args, os.Stdin, ptysuspend.Options{
		Enabled:    true,
		EscapeArgv: []string{defaultShell()},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown pty-suspend: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

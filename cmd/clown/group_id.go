package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"code.linenisgreat.com/clown/internal/clownfile"
)

// groupIDCommandTimeout bounds the boot-time [attach].group-id-command so a
// wedged orchestrator query degrades to "ungrouped" instead of stalling launch.
const groupIDCommandTimeout = 3 * time.Second

// resolveGroupID resolves the session's RFC-0014 group key: the env-interpolated
// [attach].group-id when non-empty, else the output of
// [attach].group-id-command (§2.3, clown#236). The fallback exists for a
// session no orchestrator launched — e.g. a shell-launched clown in a main
// checkout, whose orchestrator session is only materialized later by a hook
// inside the provider, too late to reach clown's environment. Any failure of
// the command leaves the session ungrouped; it never fails the launch.
func resolveGroupID(a clownfile.Attach, cwd, sessionID string, run func(argv []string) (string, error)) string {
	if g := clownfile.ResolveEnv(a.GroupID); g != "" {
		return g
	}
	argv, err := a.ResolveGroupIDCommand(cwd, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v (session left ungrouped)\n", err)
		return ""
	}
	if argv == nil {
		return ""
	}
	out, err := run(argv)
	if err != nil {
		return ""
	}
	return parseGroupIDCommandOutput(out)
}

// parseGroupIDCommandOutput accepts exactly one non-blank line with no interior
// whitespace; anything else is treated as "no group" rather than risking a
// garbled key leaking into presence, chat channels, and derived rooms.
func parseGroupIDCommandOutput(out string) string {
	g := strings.TrimSpace(out)
	if g == "" || strings.ContainsAny(g, " \t\r\n") {
		return ""
	}
	return g
}

// runGroupIDCommand executes argv with stdin and stderr detached and returns its
// stdout. A missing binary, a non-zero exit, or the timeout is an error.
func runGroupIDCommand(argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), groupIDCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	return string(out), err
}

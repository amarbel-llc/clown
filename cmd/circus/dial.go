package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// dialClient connects to the ringmaster control socket and returns a
// usable Client. On a missing-socket or connection-refused error it
// prints a one-paragraph hint to stderr pointing the user at the
// home-manager option. The error is still returned for the caller to
// propagate; the stderr message is purely UX so users can self-serve
// the fix.
func dialClient() (*rm.Client, error) {
	socket, err := rm.SocketPath()
	if err != nil {
		return nil, err
	}
	// Stat the socket first. If the file doesn't exist, the daemon
	// is definitively not running — emit the hint and bail out without
	// even attempting the dial. This also dodges platform quirks like
	// macOS returning EINVAL (path-too-long) instead of ENOENT for
	// nonexistent paths beyond the 104-byte sun_path limit.
	if _, statErr := os.Stat(socket); errors.Is(statErr, fs.ErrNotExist) {
		printNotRunningHint()
		return nil, fmt.Errorf("ringmaster socket %s does not exist", socket)
	}
	cli, err := rm.NewClient(socket)
	if err == nil {
		return cli, nil
	}
	if isConnectionRefused(err) {
		printNotRunningHint()
	}
	return nil, err
}

// printNotRunningHint writes the canonical "ringmaster is not running"
// fix-it message to stderr. Centralised so the missing-socket and
// connection-refused branches stay byte-identical.
func printNotRunningHint() {
	fmt.Fprintf(os.Stderr,
		"circus: ringmaster is not running.\n"+
			"  fix: enable it in your home-manager config:\n"+
			"    programs.ringmaster.enable = true;\n"+
			"  then run: home-manager switch\n",
	)
}

// isConnectionRefused is true when err looks like "the socket file
// exists but nobody is listening." We treat that as the same UX state
// as a missing socket — the daemon is not running.
func isConnectionRefused(err error) bool {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		// "connect: connection refused" comes through as OpError
		// wrapping a syscall.Errno. Match by string for portability
		// across linux/darwin since the Errno constants differ.
		if strings.Contains(oe.Err.Error(), "refused") {
			return true
		}
	}
	return false
}

// Command troupe is the messaging surface of the clown job platform (RFC-0015):
// a standalone binary carrying cross-session chat (send/read/list, RFC-0013 §3)
// and the standalone waking message (promoted from `clown job message`), plus
// the troupe MCP tool surface. Its current transport is the RFC-0009 wake
// channel journal; the CLI is specified transport-abstract (RFC-0015 §4) so a
// future XMPP backend can replace the journal without changing the surface.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: troupe <command> [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  send --target KEY --message MSG               send a chat message (git-commit format)")
	fmt.Fprintln(w, "  read [--peek] [--json]                        read chat addressed to this session")
	fmt.Fprintln(w, "  list [--json]                                list live chat recipients (presence)")
	fmt.Fprintln(w, "  message --target KEY --message MSG            emit a standalone waking message")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  internal (synthesized by clown, not run by hand):")
	fmt.Fprintln(w, "  mcp                                          serve the troupe MCP tool surface")
}

func run(args []string) int {
	if len(args) < 1 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "send":
		return troupeSend(args[1:])
	case "read":
		return troupeRead(args[1:])
	case "list":
		return troupeList(args[1:])
	case "message":
		// The standalone waking message is an emit: no-op with exit 0 when the
		// facility is disabled so producers need no conditional logic (RFC-0009 §8).
		if jobWakeupDisabled() {
			return 0
		}
		return troupeMessage(args[1:])
	case "mcp":
		return troupeMCP(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "troupe: unknown command %q\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

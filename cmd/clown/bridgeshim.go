package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"code.linenisgreat.com/clown/internal/0/bridgewire"
	"code.linenisgreat.com/clown/internal/alfa/bridgeshim"
)

// The inside half of the bridge (FDR 0048, contract v1; internal/*/bridgeshim).
// Two hidden verbs, started by clown's own launch of a session that uses the
// bridge (nothing in an ordinary launch starts them yet: off by default):
//
//	clown bridge-stdio --service SVC [--socket S]
//	    stdin/stdout to one stream of service SVC: an MCP server entry in claude's
//	    config, or a hook command. S defaults to $CLOWN_BRIDGE_SOCKET.
//	clown bridge-listen [--socket S] [--tcp ADDR=SVC]... [--unix PATH=SVC]...
//	    the endpoints programs already know (a loopback port, an agent socket path)
//	    forwarded to services; it opens the `control` stream and, when that ends,
//	    refuses every endpoint (the down or unusable text). Prints on stdout
//	    "bridge-listen: ready control" once the control stream is held, or
//	    "bridge-listen: ready down" once the attempt is over without it.

// bridgeSocketFlag returns the socket from --socket or the environment.
func bridgeSocket(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(bridgeshim.EnvSocket)
}

func runBridgeStdio(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var sock, svc string
	for len(args) > 0 {
		if len(args) < 2 {
			fmt.Fprintf(stderr, "clown bridge-stdio: %s wants a value\n", args[0])
			return 2
		}
		switch args[0] {
		case "--socket":
			sock = args[1]
		case "--service":
			svc = args[1]
		default:
			fmt.Fprintf(stderr, "clown bridge-stdio: unknown argument %q\n", args[0])
			return 2
		}
		args = args[2:]
	}
	sock = bridgeSocket(sock)
	if sock == "" || svc == "" {
		fmt.Fprintln(stderr, "usage: clown bridge-stdio --service SVC [--socket S]   (S defaults to $"+bridgeshim.EnvSocket+")")
		return 2
	}
	if err := bridgeshim.Stdio(sock, svc, stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	return 0
}

// parseBridgeListenArgs reads the verb's arguments.
func parseBridgeListenArgs(args []string) (sock string, eps []bridgeshim.Endpoint, err error) {
	for len(args) > 0 {
		if len(args) < 2 {
			return "", nil, fmt.Errorf("%s wants a value", args[0])
		}
		switch args[0] {
		case "--socket":
			sock = args[1]
		case "--tcp", "--unix":
			addr, svc, ok := strings.Cut(args[1], "=")
			if !ok || addr == "" || svc == "" {
				return "", nil, fmt.Errorf("%s wants ADDR=SERVICE, got %q", args[0], args[1])
			}
			network := "tcp"
			if args[0] == "--unix" {
				network = "unix"
			}
			eps = append(eps, bridgeshim.Endpoint{Network: network, Addr: addr, Service: svc})
		default:
			return "", nil, fmt.Errorf("unknown argument %q", args[0])
		}
		args = args[2:]
	}
	if sock = bridgeSocket(sock); sock == "" {
		return "", nil, errors.New("no bridge socket (--socket or $" + bridgeshim.EnvSocket + ")")
	}
	// No endpoint is valid: the verb then only holds the control stream, which
	// is what a session with a bridge but no service endpoints yet needs (the
	// down text still reaches the log).
	return sock, eps, nil
}

func runBridgeListen(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return runBridgeListenCtx(ctx, args, stdout, stderr)
}

// runBridgeListenCtx is the verb until ctx ends. The control stream is opened
// in the background, with the retry window in bridgeshim.Control (a server that
// cannot yet admit this unit answers `refused` for a moment, round 1, H1), and
// the ready line waits for the outcome: `ready control` once it is held,
// `ready down` once the window ran out or the stream failed.
func runBridgeListenCtx(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	sock, eps, err := parseBridgeListenArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "clown bridge-listen: %v\nusage: clown bridge-listen [--socket S] [--tcp ADDR=SVC]... [--unix PATH=SVC]...\n", err)
		return 2
	}
	sh := &bridgeshim.Shim{Sock: sock, Log: stderr}
	if err := sh.Listen(eps); err != nil {
		fmt.Fprintf(stderr, "clown bridge-listen: %v\n", err)
		return 1
	}
	defer sh.Close()
	// The control stream: its end means the bridge is down. A bridge that
	// cannot be reached after the retry window is down from the start.
	opened := make(chan struct{}, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		reason, err := bridgeshim.ControlOpen(ctx, sock, func() {
			sh.SetControlHeld(true)
			sh.ClearDown() // a dial that failed before control opened does not outlive it
			opened <- struct{}{}
		}, func(bridgeshim.Frame) {})
		var down *bridgeshim.DownError
		var refused *bridgeshim.RefusedError
		switch {
		case errors.As(err, &down):
			sh.MarkDown(down.Reason)
		case errors.As(err, &refused) && refused.Code == bridgewire.CodeVersion:
			// One side was upgraded alone: the bridge is unusable (round 1, M5).
			sh.MarkUnusable(err.Error())
		case err != nil:
			// A refusal of the control service itself (a server without one, or
			// one that still will not admit this unit after the window): without
			// `control` the shim can never be told when the bridge goes, which is
			// the "looks working" case, so the bridge is unusable from here
			// (joint round, J-M3 row 3). The reason is the last refusal; what to
			// check is in the contract's unusable text.
			sh.MarkUnusable(err.Error())
		case ctx.Err() == nil:
			sh.SetControlHeld(false)
			sh.MarkDown(reason)
		}
	}()
	// "ready control" means the control stream is held; "ready down" means the
	// attempt is over without it (the retry window ran out and the down line is
	// already said). The provider starts only after this line, so no dial of its
	// own can come before the server can admit the unit (round 2, M2); a
	// required bridge refuses the launch on "ready down" (joint round, J-M4).
	held := false
	select {
	case <-opened:
		held = true
	case <-finished:
		select {
		case <-opened:
			held = true // opened and ended at once
		default:
		}
	case <-ctx.Done():
	}
	if held {
		fmt.Fprintln(stdout, bridgeReadyControl)
	} else {
		fmt.Fprintln(stdout, bridgeReadyDown)
	}
	<-ctx.Done()
	return 0
}

// The two ready lines of `bridge-listen` (its stdout, read by agent-exec).
const (
	bridgeReadyControl = "bridge-listen: ready control"
	bridgeReadyDown    = "bridge-listen: ready down"
)

// parseBridgeReady reads bridge-listen's first stdout line: whether `control` is
// held. Anything else (an exit before the line, a line from another version) is
// an error, never "held".
func parseBridgeReady(line string) (held bool, err error) {
	switch strings.TrimSpace(line) {
	case bridgeReadyControl:
		return true, nil
	case bridgeReadyDown:
		return false, nil
	}
	return false, errors.New("bridge-listen exited before it was ready")
}

// bridgeGate decides what agent-exec does with the listener's answer. A
// bridge that is not required never refuses (the down text and the result
// already say what is wrong). A required one refuses when `control` is not
// held, or the listener never answered: the unit's only way out is unusable.
func bridgeGate(required, held bool, err error) (refuse bool, msg string) {
	switch {
	case required && err != nil:
		return true, fmt.Sprintf("the bridge is required and its listener did not come up: %v", err)
	case required && !held:
		return true, "the bridge is required and its control stream did not open; refusing to start the provider"
	}
	return false, ""
}

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
//	    answers every endpoint with the bridge-is-down text. Prints
//	    "bridge-listen: ready" on stdout once every endpoint listens.

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// The control stream: its end means the bridge is down. A bridge that
	// cannot be reached at start is down from the start.
	go func() {
		reason, err := bridgeshim.Control(ctx, sock, func(bridgeshim.Frame) {})
		var down *bridgeshim.DownError
		switch {
		case errors.As(err, &down):
			sh.MarkDown(down.Reason)
		case err != nil:
			// A refusal of the control service itself (a server without one): the
			// shim cannot tell when the bridge goes; it says so once and carries on.
			fmt.Fprintf(stderr, "clown bridge-listen: no control stream (%v); the bridge's end will only show on the next connection\n", err)
		case ctx.Err() == nil:
			sh.MarkDown(reason)
		}
	}()
	fmt.Fprintln(stdout, "bridge-listen: ready")
	<-ctx.Done()
	return 0
}

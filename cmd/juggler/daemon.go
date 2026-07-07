package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	rm "github.com/amarbel-llc/clown/internal/juggler"
	"github.com/amarbel-llc/clown/internal/jugglermodels"
)

// runDaemon runs the llama-server control-plane daemon (FDR-0010), invoked as
// `juggler daemon`. The daemon stays clown-side; the job platform it once
// shared a binary with (jobwake + jobmcp) was extracted into the standalone
// github.com/amarbel-llc/ringmaster module. args are the arguments following
// the `daemon` subcommand.
func runDaemon(args []string) int {
	socket := ""
	// llamaServer defaults to the build-time path (burned in via the
	// juggler-owned LlamaServerPath ldflag in flake.nix). --llama-server
	// overrides it; tests use this to point at the fake server
	// fixture. Dev builds (go build / go run) leave LlamaServerPath
	// empty, so the daemon errors out clearly instead of constructing
	// a nil launcher and serving "launcher not configured" forever.
	llamaServer := LlamaServerPath
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--socket" && i+1 < len(args):
			socket = args[i+1]
			i++
		case args[i] == "--llama-server" && i+1 < len(args):
			llamaServer = args[i+1]
			i++
		}
	}
	if socket == "" {
		var err error
		socket, err = rm.SocketPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "juggler:", err)
			return 1
		}
	}
	if llamaServer == "" {
		fmt.Fprintln(os.Stderr, "juggler: llama-server path not configured; pass --llama-server PATH or use a nix-built binary")
		return 1
	}

	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "juggler:", err)
		return 1
	}
	// Stale socket cleanup. If a previous daemon crashed, the file
	// remains; net.Listen("unix") refuses to bind over it.
	_ = os.Remove(socket)

	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "juggler: listen:", err)
		return 1
	}
	defer os.Remove(socket)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigCh; cancel() }()

	reg := rm.NewRegistry()
	launcher := newLauncher(llamaServer, reg, jugglermodels.Dir())
	srv := newServer(reg, launcher)
	fmt.Fprintln(os.Stderr, "juggler: daemon listening on", socket)
	fmt.Fprintln(os.Stderr, "juggler: llama-server", llamaServer)
	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintln(os.Stderr, "juggler:", err)
		return 1
	}
	return 0
}

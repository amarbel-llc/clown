package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/amarbel-llc/clown/internal/buildcfg"
	"github.com/amarbel-llc/clown/internal/circusmodels"
	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// runDaemon runs the llama-server control-plane daemon (FDR-0010). It was
// formerly the `ringmaster daemon` verb; the job platform moved to the
// standalone ringmaster module, so the daemon re-homed here and now runs as
// `circus daemon` (clown owns llama-server; ringmaster is the job platform
// only). args are the arguments following the `daemon` subcommand.
func runDaemon(args []string) int {
	socket := ""
	// llamaServer defaults to the build-time path (burned in via
	// buildcfg.LlamaServerPath in flake.nix). --llama-server
	// overrides it; tests use this to point at the fake server
	// fixture. Dev builds (go build / go run) leave LlamaServerPath
	// empty, so the daemon errors out clearly instead of constructing
	// a nil launcher and serving "launcher not configured" forever.
	llamaServer := buildcfg.LlamaServerPath
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
			fmt.Fprintln(os.Stderr, "circus:", err)
			return 1
		}
	}
	if llamaServer == "" {
		fmt.Fprintln(os.Stderr, "circus: llama-server path not configured; pass --llama-server PATH or use a nix-built binary")
		return 1
	}

	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "circus:", err)
		return 1
	}
	// Stale socket cleanup. If a previous daemon crashed, the file
	// remains; net.Listen("unix") refuses to bind over it.
	_ = os.Remove(socket)

	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "circus: listen:", err)
		return 1
	}
	defer os.Remove(socket)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigCh; cancel() }()

	reg := rm.NewRegistry()
	launcher := newLauncher(llamaServer, reg, circusmodels.Dir())
	srv := newServer(reg, launcher)
	fmt.Fprintln(os.Stderr, "circus: daemon listening on", socket)
	fmt.Fprintln(os.Stderr, "circus: llama-server", llamaServer)
	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintln(os.Stderr, "circus:", err)
		return 1
	}
	return 0
}

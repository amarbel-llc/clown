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

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: ringmaster <command> [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  daemon [--socket PATH] [--llama-server PATH]   run the llama-server control-plane daemon")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  operator (out-of-band job inspection):")
	fmt.Fprintln(w, "  ls [--target KEY] [--all] [--json]            list background jobs")
	fmt.Fprintln(w, "  status <job-id> [--target KEY] [--tail N]     show one job's status and output tail")
	fmt.Fprintln(w, "  tail <job-id> [--target KEY] [-f] [-n N]      print (and optionally follow) a job's output")
	fmt.Fprintln(w, "  cancel <job-id> [--target KEY] [--message M]  cooperatively cancel a job")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  producer (job lifecycle on the wake channel):")
	fmt.Fprintln(w, "  start [--target KEY] [--label L] [--source S] allocate a job; prints the job id")
	fmt.Fprintln(w, "  progress <job-id> [--message M]              append a journal-only progress record")
	fmt.Fprintln(w, "  done <job-id> --state STATE [--message M]     append the terminal record and wake")
	fmt.Fprintln(w, "  read [--job ID] [--since TS] [--type T]       read this session's channel or one job")
	fmt.Fprintln(w, "  spool-path <job-id> [--target KEY]            print a job's output-spool path")
	fmt.Fprintln(w, "  wait <job-id> [--timeout D]                   block until the job is terminal")
	fmt.Fprintln(w, "  whoami [--json]                              print this session's key and channels")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  internal (synthesized by clown, not run by hand):")
	fmt.Fprintln(w, "  monitor [--session KEY] [--once]             run the per-session wake monitor")
	fmt.Fprintln(w, "  mcp                                          serve the ringmaster MCP tool surface")
}

func run(args []string) int {
	if len(args) < 1 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "daemon":
		return runDaemon(args[1:])
	case "ls":
		return ringmasterLs(args[1:])
	case "status":
		return ringmasterStatus(args[1:])
	case "tail":
		return ringmasterTail(args[1:])
	case "cancel":
		return ringmasterCancel(args[1:])
	// Producer verbs (RFC-0015 §2, promoted from `clown job`). The emit verbs
	// (start/progress/done) no-op with exit 0 when the facility is disabled so
	// producers need no conditional logic (RFC-0009 §8); spool-path returns empty
	// + exit 0 when disabled (RFC-0010 §2). read/wait/whoami are reads, available
	// regardless of the disable switch.
	case "start":
		if jobWakeupDisabled() {
			return 0
		}
		return ringmasterStart(args[1:])
	case "progress":
		if jobWakeupDisabled() {
			return 0
		}
		return ringmasterProgress(args[1:])
	case "done":
		if jobWakeupDisabled() {
			return 0
		}
		return ringmasterDone(args[1:])
	case "read":
		return ringmasterRead(args[1:])
	case "spool-path":
		if jobWakeupDisabled() {
			return 0
		}
		return ringmasterSpoolPath(args[1:])
	case "wait":
		return ringmasterWait(args[1:])
	case "whoami":
		return ringmasterWhoami(args[1:])
	// Internal entrypoints synthesized by clown (RFC-0015 §6).
	case "monitor":
		return ringmasterMonitor(args[1:])
	case "mcp":
		return ringmasterMCP(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "ringmaster: unknown command %q\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

// runDaemon runs the llama-server control-plane daemon (FDR-0010). args are the
// arguments following the `daemon` subcommand.
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
			fmt.Fprintln(os.Stderr, "ringmaster:", err)
			return 1
		}
	}
	if llamaServer == "" {
		fmt.Fprintln(os.Stderr, "ringmaster: llama-server path not configured; pass --llama-server PATH or use a nix-built binary")
		return 1
	}

	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "ringmaster:", err)
		return 1
	}
	// Stale socket cleanup. If a previous daemon crashed, the file
	// remains; net.Listen("unix") refuses to bind over it.
	_ = os.Remove(socket)

	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ringmaster: listen:", err)
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
	fmt.Fprintln(os.Stderr, "ringmaster: listening on", socket)
	fmt.Fprintln(os.Stderr, "ringmaster: llama-server", llamaServer)
	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintln(os.Stderr, "ringmaster:", err)
		return 1
	}
	return 0
}

// Command clown-stdio-bridge wraps a stdio MCP server and exposes it
// over streamable-HTTP, speaking the clown plugin protocol handshake on
// its own stdout. It is invoked by clown-plugin-host as the synthesized
// command for any stdioServers entry in clown.json (see FDR 0002).
//
// Skeleton implementation (commit 2 of #28): handshake, /healthz, and
// SIGTERM forwarding work end-to-end. The /mcp endpoint returns 501
// until the streamable-HTTP MCP translation lands in commit 3.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const stopGracePeriod = 5 * time.Second

func main() {
	parsed, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(parsed))
}

type parsedArgs struct {
	command string
	args    []string
}

func parseArgs(args []string) (parsedArgs, error) {
	var p parsedArgs
	i := 0
	for i < len(args) {
		switch {
		case args[i] == "--":
			p.args = append([]string(nil), args[i+1:]...)
			i = len(args)
		case args[i] == "--command":
			if i+1 >= len(args) {
				return p, fmt.Errorf("--command requires an argument")
			}
			p.command = args[i+1]
			i += 2
		default:
			return p, fmt.Errorf("unknown flag %q (expected --command or --)", args[i])
		}
	}
	if p.command == "" {
		return p, fmt.Errorf("--command is required")
	}
	return p, nil
}

func run(p parsedArgs) int {
	cmdPath, err := exec.LookPath(p.command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: locate %q: %v\n", p.command, err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdPath, p.args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr

	// stdin and stdout are wired to the MCP translator in commit 3.
	// For the skeleton: drain stdout so the wrapped child doesn't
	// backpressure on a full pipe, and leave stdin held open via the
	// pipe handle (closing it would EOF the child, which kills
	// well-behaved servers that block on stdin — e.g. `cat`).
	if _, err := cmd.StdinPipe(); err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: stdin pipe: %v\n", err)
		return 1
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: stdout pipe: %v\n", err)
		return 1
	}
	go drain(stdout)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: start %q: %v\n", cmdPath, err)
		return 1
	}

	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: listen: %v\n", err)
		terminate(cmd, childDone)
		return 1
	}

	// Handshake format must match internal/pluginhost/handshake.go.
	fmt.Printf("1|1|tcp|%s|streamable-http\n", ln.Addr().String())
	_ = os.Stdout.Sync()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-childDone:
			http.Error(w, "wrapped child has exited", http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "MCP translation not yet implemented", http.StatusNotImplemented)
	})

	srv := &http.Server{Handler: mux}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	exit := 0
	terminateChild := false
	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: received %s; shutting down\n", sig)
		terminateChild = true
	case err := <-childDone:
		// Re-fill the channel so terminate() doesn't block if it's
		// invoked elsewhere (defensive — it's not, in this path).
		childDone <- err
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown-stdio-bridge: wrapped child exited unexpectedly: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "clown-stdio-bridge: wrapped child exited cleanly")
		}
		exit = 1
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown-stdio-bridge: HTTP serve error: %v\n", err)
			exit = 1
		}
		terminateChild = true
	}

	if terminateChild {
		terminate(cmd, childDone)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	return exit
}

// drain reads r line-by-line and discards every line. Replaced by the
// stdout reader half of the JSON-RPC translator in commit 3.
func drain(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		_ = scanner.Text()
	}
}

// terminate sends SIGTERM to the wrapped child's process group and
// waits for the wait-goroutine on childDone. On timeout, escalates to
// SIGKILL and waits again. childDone MUST not have been received from
// before this call.
func terminate(cmd *exec.Cmd, childDone <-chan error) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	sendSignalToGroup(cmd, pid, syscall.SIGTERM)
	select {
	case <-childDone:
		return
	case <-time.After(stopGracePeriod):
		fmt.Fprintf(os.Stderr,
			"clown-stdio-bridge: wrapped child did not exit within %s; killing\n",
			stopGracePeriod)
		sendSignalToGroup(cmd, pid, syscall.SIGKILL)
		<-childDone
	}
}

func sendSignalToGroup(cmd *exec.Cmd, pid int, sig syscall.Signal) {
	if pgid, err := syscall.Getpgid(pid); err == nil {
		_ = syscall.Kill(-pgid, sig)
		return
	}
	_ = cmd.Process.Signal(sig)
}

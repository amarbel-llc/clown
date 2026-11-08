// Command clown-stdio-bridge wraps a stdio MCP server and exposes it
// over streamable-HTTP, speaking the clown plugin protocol handshake on
// its own stdout. It is invoked by clown-plugin-host as the synthesized
// command for any stdioServers entry in clown.json (see FDR 0002).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/0/disconnectlog"
	"code.linenisgreat.com/clown/internal/0/peercheck"
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
	// listenUnix and peerUnit (F12, FDR 0020): serve on this unix socket,
	// admitting only callers in the agent unit peerUnit (and the parent
	// clown), instead of on loopback TCP. Both or neither.
	listenUnix string
	peerUnit   string
}

func parseArgs(args []string) (parsedArgs, error) {
	var p parsedArgs
	i := 0
	for i < len(args) {
		switch {
		case args[i] == "--":
			p.args = append([]string(nil), args[i+1:]...)
			i = len(args)
		case args[i] == "--command" || args[i] == "--listen-unix" || args[i] == "--peer-unit":
			if i+1 >= len(args) {
				return p, fmt.Errorf("%s requires an argument", args[i])
			}
			switch args[i] {
			case "--command":
				p.command = args[i+1]
			case "--listen-unix":
				p.listenUnix = args[i+1]
			default:
				p.peerUnit = args[i+1]
			}
			i += 2
		default:
			return p, fmt.Errorf("unknown flag %q (expected --command, --listen-unix, --peer-unit or --)", args[i])
		}
	}
	if p.command == "" {
		return p, fmt.Errorf("--command is required")
	}
	if (p.listenUnix == "") != (p.peerUnit == "") {
		return p, fmt.Errorf("--listen-unix and --peer-unit go together")
	}
	if p.listenUnix != "" && !filepath.IsAbs(p.listenUnix) {
		return p, fmt.Errorf("--listen-unix %q must be an absolute path", p.listenUnix)
	}
	return p, nil
}

// listen opens the bridge's listener: loopback TCP, or with --listen-unix a
// unix socket whose Accept admits only the agent unit and the parent clown
// (peercheck.ListenServeSocket). It returns the handshake's network type.
func listen(p parsedArgs) (net.Listener, string, error) {
	if p.listenUnix == "" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		return ln, "tcp", err
	}
	logger := log.New(os.Stderr, "clown-stdio-bridge: ", 0)
	ln, err := peercheck.ListenServeSocket(p.listenUnix, p.peerUnit, os.Getppid, nil, logger.Printf)
	return ln, "unix", err
}

// parentPollInterval paces watchParent.
const parentPollInterval = time.Second

// watchParent calls onGone once getppid no longer returns initial (the
// parent clown died and the bridge was reparented), then returns. A serve
// bridge admits its parent by pid, so it must not outlive it: a process that
// later took the dead parent's pid would otherwise be admitted.
func watchParent(ctx context.Context, initial int, getppid func() int, interval time.Duration, onGone func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if getppid() != initial {
				onGone()
				return
			}
		}
	}
}

// handshakeLine is the plugin-protocol handshake; its format must match
// internal/*/pluginhost/handshake.go.
func handshakeLine(network, addr string) string {
	return fmt.Sprintf("1|1|%s|%s|streamable-http", network, addr)
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
	// The child's stderr goes through a pipe the bridge reads, so it can keep a
	// scrubbed tail for the disconnect record (clown#344) while copying every
	// byte on to its own stderr, which the host logs as before. If the pipe
	// cannot be made the child keeps the bridge's stderr and the record has no
	// tail.
	tail := disconnectlog.NewRing()
	stderrR, stderrW, pipeErr := os.Pipe()
	if pipeErr == nil {
		cmd.Stderr = stderrW
	} else {
		cmd.Stderr = os.Stderr
	}
	closeStderrPipe := func() {
		if pipeErr == nil {
			_ = stderrR.Close()
			_ = stderrW.Close()
		}
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: stdin pipe: %v\n", err)
		closeStderrPipe()
		return 1
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: stdout pipe: %v\n", err)
		closeStderrPipe()
		return 1
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: start %q: %v\n", cmdPath, err)
		closeStderrPipe()
		return 1
	}
	if pipeErr == nil {
		// The child holds the write end now; ours must close for EOF to arrive
		// when the child (and anything it started) is gone.
		_ = stderrW.Close()
		go func() {
			defer stderrR.Close()
			teeStderr(stderrR, os.Stderr, tail)
		}()
	}

	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()

	ln, network, err := listen(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: listen: %v\n", err)
		terminate(cmd, childDone)
		return 1
	}

	fmt.Println(handshakeLine(network, ln.Addr().String()))
	_ = os.Stdout.Sync()

	stdLogger := log.New(os.Stderr, "", 0)
	tr := newTranslator(stdin, stdout, stdLogger)
	// The disconnect record (clown#344); nil, and so a no-op, unless the host
	// gave this bridge a session, a plugin and a record directory.
	rec := newRecorder(os.Getenv, cmd.Process.Pid, tr, tail, stdLogger)
	stdinFail := make(chan struct{}, 1)
	tr.onStdinFail = func() {
		select {
		case stdinFail <- struct{}{}:
		default:
		}
	}
	go func() {
		if err := tr.Run(ctx); err != nil {
			stdLogger.Printf("clown-stdio-bridge: translator: %v", err)
		}
	}()

	handler := newHTTPHandler(tr, stdLogger, newStatsdFromEnv())
	serveMode := network == "unix"
	srv := &http.Server{Handler: newMux(handler, tr, childDone, serveMode)}
	if serveMode {
		srv.ConnContext = serveConnContext
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	parentGone := make(chan struct{})
	if network == "unix" {
		go watchParent(ctx, os.Getppid(), os.Getppid, parentPollInterval, func() { close(parentGone) })
	}

	exit := 0
	terminateChild := false
	// reportChildExit is the child-exit path, shared by the exit itself and by
	// the pipe events that arrive just ahead of it.
	reportChildExit := func(err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown-stdio-bridge: wrapped child exited unexpectedly: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "clown-stdio-bridge: wrapped child exited cleanly")
		}
		rec.record(disconnectlog.KindChildExit, disconnectlog.ClosedByChild, exitOf(err))
		exit = 1
	}
	// pipeEvent handles the child's stdout or stdin ending: a child that is
	// dying shows these within milliseconds of its exit, so the exit gets a
	// moment to arrive and is what the record then says; a child that is still
	// alive with a dead pipe is unusable, so the bridge records that and stops
	// it, as for an exit.
	pipeEvent := func(kind string) {
		if exited, err := awaitChild(childDone, childSettle); exited {
			reportChildExit(err)
			return
		}
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: wrapped child is alive but its pipe ended (%s); stopping it\n", kind)
		rec.record(kind, disconnectlog.ClosedByChild, nil)
		terminateChild = true
		exit = 1
	}
	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "clown-stdio-bridge: received %s; shutting down\n", sig)
		// The host says so before the SIGTERM that ends a session; any other
		// signal is a disconnect worth a record.
		if !handler.shutdownNoticed.Load() {
			rec.record(disconnectlog.KindSignal, disconnectlog.ClosedByExternal, nil)
		}
		terminateChild = true
	case <-parentGone:
		fmt.Fprintln(os.Stderr, "clown-stdio-bridge: parent clown is gone; shutting down the serve socket")
		rec.record(disconnectlog.KindParentGone, disconnectlog.ClosedByHost, nil)
		terminateChild = true
		exit = 1
	case err := <-childDone:
		// Re-fill the channel so terminate() doesn't block if it's
		// invoked elsewhere (defensive — it's not, in this path).
		childDone <- err
		reportChildExit(err)
	case <-tr.readerDone:
		pipeEvent(disconnectlog.KindChildStdoutEOF)
	case <-stdinFail:
		pipeEvent(disconnectlog.KindChildStdinClosed)
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown-stdio-bridge: HTTP serve error: %v\n", err)
			rec.record(disconnectlog.KindHTTPServeError, disconnectlog.ClosedByBridge, nil)
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

// childSettle is how long a pipe event waits for the child's exit to arrive
// before the record says the child is still alive.
const childSettle = 300 * time.Millisecond

// awaitChild waits up to d for the child to exit. It leaves the exit status in
// childDone for terminate and the caller.
func awaitChild(childDone chan error, d time.Duration) (exited bool, err error) {
	select {
	case err := <-childDone:
		childDone <- err
		return true, err
	case <-time.After(d):
		return false, nil
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

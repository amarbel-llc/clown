package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	rm "code.linenisgreat.com/clown/internal/juggler"
)

const (
	healthTimeout  = 60 * time.Second
	healthInterval = 250 * time.Millisecond
	stopGrace      = 5 * time.Second
)

// child wraps a running llama-server process. The OS only permits a
// single cmd.Wait() call per child, so the launcher owns that call in
// a single goroutine started at Start time and publishes the result
// via the exited channel. Stop (and the future reaper in Task 12)
// both observe completion by reading exited rather than calling Wait
// themselves.
type child struct {
	cmd     *exec.Cmd
	exited  chan struct{} // closed when cmd.Wait returns
	waitErr error         // populated before exited is closed
}

// llauncher is the real Launcher implementation. It runs llama-server
// children, registers them, and reaps them on Stop. The mu/children
// map are kept separate from the Registry: the Registry holds the
// public Instance view; children holds the os/exec.Cmd we'll signal.
type llauncher struct {
	llamaServerPath string
	reg             *rm.Registry
	modelsDir       string

	mu       sync.Mutex
	children map[string]*child // alias → process handle
	// starting holds the Starts in progress by alias: a second Start of
	// one alias fails before spawning (clown#312), and Stop stops a
	// pending start (clown#313).
	starting map[string]*pendingStart

	// afterHealthy, when set, is called with the start's ctx once a child
	// has turned healthy, before it is registered (tests).
	afterHealthy func(context.Context, *child)
}

func newLauncher(binary string, reg *rm.Registry, modelsDir string) *llauncher {
	return &llauncher{
		llamaServerPath: binary,
		reg:             reg,
		modelsDir:       modelsDir,
		children:        make(map[string]*child),
		starting:        make(map[string]*pendingStart),
	}
}

// pendingStart is a Start in progress: Stop cancels it through cancel and
// waits on done, closed once that Start has returned.
type pendingStart struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

var errStoppedWhileStarting = errors.New("stopped while starting")

// pendingStopWait bounds how long Stop waits for a cancelled start to end.
// A cancelled start ends at once (its health wait returns, its child is
// SIGKILLed), so this only guards against a wedged one.
const pendingStopWait = 2 * stopGrace

// reserveAlias claims alias for one Start. It fails if another Start holds
// it or a child already runs under it.
func (l *llauncher) reserveAlias(alias string, cancel context.CancelCauseFunc) (*pendingStart, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.starting[alias]; ok {
		return nil, fmt.Errorf("alias %q is already starting", alias)
	}
	// The registry outlives children during Stop's grace window (children
	// cleared first, the registry after the exit): still running there.
	if _, ok := l.children[alias]; ok {
		return nil, fmt.Errorf("alias %q is already running", alias)
	}
	if _, ok := l.reg.Get(alias); ok {
		return nil, fmt.Errorf("alias %q is already running", alias)
	}
	ps := &pendingStart{cancel: cancel, done: make(chan struct{})}
	l.starting[alias] = ps
	return ps, nil
}

// endStart drops a Start's reservation, unless register already handed it
// to children, and then marks the Start done.
func (l *llauncher) endStart(alias string, ps *pendingStart) {
	l.mu.Lock()
	if l.starting[alias] == ps {
		delete(l.starting, alias)
	}
	l.mu.Unlock()
	close(ps.done)
}

// PendingStarts lists the aliases with a Start in progress, which StopAll
// stops along with the registered instances.
func (l *llauncher) PendingStarts() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	aliases := make([]string, 0, len(l.starting))
	for alias := range l.starting {
		aliases = append(aliases, alias)
	}
	return aliases
}

// startCancelled is the error for a start whose ctx is done: the client
// left, or Stop stopped it.
func startCancelled(ctx context.Context, alias, when string) error {
	return fmt.Errorf("start %s cancelled %s: %w", alias, when, context.Cause(ctx))
}

// Start spawns a llama-server child, waits for it to become healthy,
// and registers an Instance for the given alias. The model is
// resolved relative to modelsDir if not absolute; we do NOT stat the
// resolved path here because the real llama-server validates it and
// the test fake ignores it.
//
// The alias is reserved for the whole Start, so a concurrent Start of the
// same alias fails at once without spawning, and only this Start touches
// children[alias] until it returns. A Stop of the alias meanwhile cancels
// this Start: it kills its child and registers nothing.
func (l *llauncher) Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error) {
	ctx, cancelStart := context.WithCancelCause(ctx)
	defer cancelStart(nil)
	ps, err := l.reserveAlias(p.Alias, cancelStart)
	if err != nil {
		return rm.Instance{}, err
	}
	defer l.endStart(p.Alias, ps)

	bind := p.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}

	port, err := pickFreePort(bind)
	if err != nil {
		return rm.Instance{}, fmt.Errorf("pick port: %w", err)
	}

	var modelPath string
	if p.Model != "" {
		modelPath = p.Model
		if !filepath.IsAbs(modelPath) {
			modelPath = filepath.Join(l.modelsDir, p.Model+".gguf")
		}
	}

	args := []string{
		"--port", strconv.Itoa(port),
		"--host", bind,
		"--alias", p.Alias,
	}
	if modelPath != "" {
		args = append(args, "--model", modelPath)
	}
	args = append(args, p.Args...)

	// A start whose client already left (clown#82), or that Stop stopped,
	// spawns nothing.
	if ctx.Err() != nil {
		return rm.Instance{}, startCancelled(ctx, p.Alias, "before spawning")
	}
	cmd := exec.Command(l.llamaServerPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return rm.Instance{}, fmt.Errorf("start llama-server: %w", err)
	}

	ch := &child{cmd: cmd, exited: make(chan struct{})}
	go func() {
		ch.waitErr = cmd.Wait()
		close(ch.exited)
	}()
	go l.reapOnExit(p.Alias, ch)

	addr := fmt.Sprintf("%s:%d", bind, port)
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	if err := waitHealthy(hctx, addr); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-ch.exited
		if ctx.Err() != nil {
			return rm.Instance{}, startCancelled(ctx, p.Alias, "during the health wait")
		}
		return rm.Instance{}, fmt.Errorf("waitHealthy: %w", err)
	}
	if l.afterHealthy != nil {
		l.afterHealthy(ctx, ch)
	}
	// The client may have left, or Stop stopped the start, just as the
	// child turned healthy: it is killed and not registered. register
	// checks again under l.mu, which Stop cancels under.
	if ctx.Err() != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-ch.exited
		return rm.Instance{}, startCancelled(ctx, p.Alias, "after it turned healthy")
	}

	in := rm.Instance{
		Alias:     p.Alias,
		Model:     p.Model,
		Port:      port,
		PID:       cmd.Process.Pid,
		Bind:      bind,
		StartedAt: time.Now().UTC(),
	}

	if err := l.register(ctx, in, ch); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-ch.exited
		return rm.Instance{}, err
	}
	return in, nil
}

// register records a healthy child under one l.mu hold: the registry
// entry, the children entry, and the hand-off of the alias's reservation
// from starting to children, so no reader sees the alias half-registered.
// reapOnExit takes l.mu too (lock order l.mu, then the registry's), so a
// child that exits after register is reaped, and one that already exited
// is refused here instead of registered dead. Stop cancels a pending start
// under l.mu, so a start Stop reached first is refused here too.
func (l *llauncher) register(ctx context.Context, in rm.Instance, ch *child) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ctx.Err() != nil {
		return startCancelled(ctx, in.Alias, "before registering")
	}
	select {
	case <-ch.exited:
		return fmt.Errorf("start %s: llama-server exited after turning healthy: %v", in.Alias, ch.waitErr)
	default:
	}
	if err := l.reg.Add(in); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	l.children[in.Alias] = ch
	delete(l.starting, in.Alias)
	return nil
}

// Stop signals the child for alias to terminate. SIGTERM first, then
// SIGKILL after a grace period if it didn't exit. Removes from the
// registry on success. Wait() is owned by the goroutine launched in
// Start; Stop observes process exit via ch.exited rather than calling
// Wait directly.
//
// Stop of an alias that is still starting (clown#313) cancels that Start
// and waits, bounded, for it to end: its child is killed and nothing is
// registered, and Stop then succeeds, as for a running alias.
func (l *llauncher) Stop(ctx context.Context, alias string) error {
	l.mu.Lock()
	if ps, ok := l.starting[alias]; ok {
		ps.cancel(errStoppedWhileStarting)
		l.mu.Unlock()
		select {
		case <-ps.done:
			return nil
		case <-time.After(pendingStopWait):
			return fmt.Errorf("alias %q: its start did not end within %s of Stop", alias, pendingStopWait)
		}
	}
	ch, ok := l.children[alias]
	delete(l.children, alias)
	l.mu.Unlock()
	if !ok {
		return fmt.Errorf("alias %q not running", alias)
	}

	pgid := -ch.cmd.Process.Pid // process group
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// Already gone: nothing to signal, but the alias is still cleared.
			l.reg.Remove(alias)
			return nil
		}
		return fmt.Errorf("SIGTERM %d: %w", pgid, err)
	}

	select {
	case <-ch.exited:
	case <-time.After(stopGrace):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		<-ch.exited
	}

	l.reg.Remove(alias)
	return nil
}

// reapOnExit watches a child's exited channel and cleans up the
// registry + children map once the process has exited, but only if
// Stop hasn't already taken ownership of cleanup.
//
// The l.children[alias] == ch guard is the coordination point with
// Stop: Stop's first act is to delete(l.children, alias) under l.mu,
// so by the time Stop has run, the lookup here either misses (alias
// gone) or yields a different *child (a subsequent Start under the
// same alias). Either way the equality check fails and we no-op,
// avoiding a double Remove. If Stop hasn't run — i.e. the child died
// from a crash, OOM kill, or external SIGKILL — the entry is still
// ch and we own the cleanup.
func (l *llauncher) reapOnExit(alias string, ch *child) {
	<-ch.exited
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.children[alias] == ch {
		delete(l.children, alias)
		l.reg.Remove(alias)
	}
}

func pickFreePort(host string) (int, error) {
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// waitHealthy polls GET <addr>/health every healthInterval until 200
// or ctx is done. Returns ctx's error on timeout.
func waitHealthy(ctx context.Context, addr string) error {
	url := "http://" + addr + "/health"
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthInterval):
		}
	}
}

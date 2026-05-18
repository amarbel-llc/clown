package main

import (
	"context"
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

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

const (
	healthTimeout  = 60 * time.Second
	healthInterval = 250 * time.Millisecond
	stopGrace      = 5 * time.Second
)

// llauncher is the real Launcher implementation. It runs llama-server
// children, registers them, and reaps them on Stop. The mu/children
// map are kept separate from the Registry: the Registry holds the
// public Instance view; children holds the os/exec.Cmd we'll signal.
type llauncher struct {
	llamaServerPath string
	reg             *rm.Registry
	modelsDir       string

	mu       sync.Mutex
	children map[string]*exec.Cmd // alias → process handle
}

func newLauncher(binary string, reg *rm.Registry, modelsDir string) *llauncher {
	return &llauncher{
		llamaServerPath: binary,
		reg:             reg,
		modelsDir:       modelsDir,
		children:        make(map[string]*exec.Cmd),
	}
}

// Start spawns a llama-server child, waits for it to become healthy,
// and registers an Instance for the given alias. The model is
// resolved relative to modelsDir if not absolute; we do NOT stat the
// resolved path here because the real llama-server validates it and
// the test fake ignores it.
func (l *llauncher) Start(ctx context.Context, p rm.StartInstanceParams) (rm.Instance, error) {
	bind := p.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}

	port, err := pickFreePort(bind)
	if err != nil {
		return rm.Instance{}, fmt.Errorf("pick port: %w", err)
	}

	modelPath := p.Model
	if !filepath.IsAbs(modelPath) {
		modelPath = filepath.Join(l.modelsDir, p.Model+".gguf")
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

	cmd := exec.Command(l.llamaServerPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return rm.Instance{}, fmt.Errorf("start llama-server: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", bind, port)
	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	if err := waitHealthy(hctx, addr); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return rm.Instance{}, fmt.Errorf("waitHealthy: %w", err)
	}

	in := rm.Instance{
		Alias:     p.Alias,
		Model:     p.Model,
		Port:      port,
		PID:       cmd.Process.Pid,
		Bind:      bind,
		StartedAt: time.Now().UTC(),
	}
	if err := l.reg.Add(in); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return rm.Instance{}, fmt.Errorf("register: %w", err)
	}

	l.mu.Lock()
	l.children[p.Alias] = cmd
	l.mu.Unlock()
	return in, nil
}

// Stop signals the child for alias to terminate. SIGTERM first, then
// SIGKILL after a grace period if it didn't exit. Removes from the
// registry on success.
func (l *llauncher) Stop(ctx context.Context, alias string) error {
	l.mu.Lock()
	cmd, ok := l.children[alias]
	delete(l.children, alias)
	l.mu.Unlock()
	if !ok {
		return fmt.Errorf("alias %q not running", alias)
	}

	pgid := -cmd.Process.Pid // process group
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM %d: %w", pgid, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(stopGrace):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		<-done // drain Wait
	}

	l.reg.Remove(alias)
	return nil
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

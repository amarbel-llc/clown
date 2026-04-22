package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/amarbel-llc/clown/internal/buildcfg"
	"github.com/amarbel-llc/clown/internal/daemon"
)

const (
	healthPath      = "/health"
	healthTimeout   = 60 * time.Second
	healthInterval  = 500 * time.Millisecond
	stopGracePeriod = 5 * time.Second
)

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "circus"), nil
}

func pidfilePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "llama-server.pid"), nil
}

func portfilePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "llama-server.port"), nil
}

// attachOrStart returns the port of a running llama-server, starting one
// if none is alive. spawned is true when this call launched the process.
func attachOrStart() (port int, spawned bool, err error) {
	pidPath, err := pidfilePath()
	if err != nil {
		return 0, false, err
	}
	portPath, err := portfilePath()
	if err != nil {
		return 0, false, err
	}

	if pid, err := daemon.ReadPID(pidPath); err == nil && daemon.IsRunning(pid) {
		if data, err := os.ReadFile(portPath); err == nil {
			if p, err := strconv.Atoi(string(data)); err == nil {
				return p, false, nil
			}
		}
	}

	// Clean up stale pidfile.
	_ = daemon.RemovePID(pidPath)

	port, err = startDaemon(pidPath, portPath)
	if err != nil {
		return 0, false, err
	}
	return port, true, nil
}

func startDaemon(pidPath, portPath string) (int, error) {
	port := 8080
	if v := os.Getenv("CIRCUS_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			port = p
		}
	}

	binary := os.Getenv("CIRCUS_LLAMA_SERVER")
	if binary == "" {
		binary = buildcfg.LlamaServerPath
	}
	if binary == "" {
		binary = "llama-server"
	}

	args := []string{
		"--port", strconv.Itoa(port),
		"--host", "127.0.0.1",
	}
	model := os.Getenv("CIRCUS_MODEL")
	if model == "" {
		model = buildcfg.DefaultModelPath
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting llama-server: %w", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	if err := waitHealthy(ctx, addr); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return 0, fmt.Errorf("llama-server health check: %w", err)
	}

	if err := daemon.WritePID(pidPath, cmd.Process.Pid); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return 0, err
	}
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(port)), 0o644); err != nil {
		return 0, err
	}

	// Detach: let llama-server outlive this process.
	_ = cmd.Process.Release()

	return port, nil
}

func stopDaemon() error {
	pidPath, err := pidfilePath()
	if err != nil {
		return err
	}
	portPath, err := portfilePath()
	if err != nil {
		return err
	}

	pid, err := daemon.ReadPID(pidPath)
	if os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "circus: llama-server is not running")
		return nil
	}
	if err != nil {
		return err
	}

	if !daemon.IsRunning(pid) {
		fmt.Fprintln(os.Stderr, "circus: llama-server is not running (stale pidfile)")
		_ = daemon.RemovePID(pidPath)
		_ = daemon.RemovePID(portPath)
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM to %d: %w", pid, err)
	}

	deadline := time.Now().Add(stopGracePeriod)
	for time.Now().Before(deadline) {
		if !daemon.IsRunning(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if daemon.IsRunning(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	_ = daemon.RemovePID(pidPath)
	_ = daemon.RemovePID(portPath)
	fmt.Fprintln(os.Stderr, "circus: llama-server stopped")
	return nil
}

func statusDaemon() error {
	pidPath, err := pidfilePath()
	if err != nil {
		return err
	}
	portPath, err := portfilePath()
	if err != nil {
		return err
	}

	pid, err := daemon.ReadPID(pidPath)
	if os.IsNotExist(err) {
		fmt.Println("not running")
		return nil
	}
	if err != nil {
		return err
	}

	if !daemon.IsRunning(pid) {
		fmt.Println("not running (stale pidfile)")
		return nil
	}

	port := 8080
	if data, err := os.ReadFile(portPath); err == nil {
		if p, err := strconv.Atoi(string(data)); err == nil {
			port = p
		}
	}

	fmt.Printf("running  pid=%d  url=http://127.0.0.1:%d\n", pid, port)
	return nil
}

func waitHealthy(ctx context.Context, addr string) error {
	url := "http://" + addr + healthPath
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

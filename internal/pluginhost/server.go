package pluginhost

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func exitSignal(state *os.ProcessState) string {
	if status, ok := state.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			return status.Signal().String()
		}
	}
	return ""
}

const stopGracePeriod = 5 * time.Second

type ManagedServer struct {
	Name      string
	Def       ServerDef
	PluginDir string
	Logger    *slog.Logger

	cmd       *exec.Cmd
	handshake Handshake
	done      chan struct{}
	waitState *os.ProcessState
}

func (s *ManagedServer) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger.With("server", s.Name)
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (s *ManagedServer) Start(ctx context.Context) error {
	log := s.logger()

	cmdPath := s.Def.Command
	if !strings.HasPrefix(cmdPath, "/") {
		cmdPath = s.PluginDir + "/" + cmdPath
	}

	s.cmd = exec.CommandContext(ctx, cmdPath, s.Def.Args...)
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	for k, v := range s.Def.Env {
		s.cmd.Env = append(os.Environ(), k+"="+v)
	}

	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("server %s: stdout pipe: %w", s.Name, err)
	}

	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("server %s: stderr pipe: %w", s.Name, err)
	}

	log.Info("starting plugin server", "command", cmdPath, "args", s.Def.Args, "plugin_dir", s.PluginDir)
	if err := s.cmd.Start(); err != nil {
		log.Error("plugin server failed to start", "err", err)
		return fmt.Errorf("server %s: start: %w", s.Name, err)
	}
	log.Info("plugin server process started", "pid", s.cmd.Process.Pid)

	go s.forwardStderr(stderr)

	hs, err := s.readHandshake(ctx, stdout)
	if err != nil {
		log.Error("handshake failed", "err", err)
		s.kill()
		return err
	}
	s.handshake = hs
	log.Info("handshake received",
		"core_version", hs.CoreVersion,
		"app_version", hs.AppVersion,
		"network_type", hs.NetworkType,
		"address", hs.Address,
		"protocol", hs.Protocol,
	)

	healthCtx, cancel := context.WithTimeout(ctx, s.Def.Healthcheck.Timeout.Duration)
	defer cancel()

	if err := WaitHealthy(healthCtx, hs.Address, s.Def.Healthcheck.Path, s.Def.Healthcheck.Interval.Duration); err != nil {
		log.Error("healthcheck failed", "err", err)
		s.kill()
		return fmt.Errorf("server %s: %w", s.Name, err)
	}
	log.Info("plugin server healthy", "url", hs.URL())

	s.done = make(chan struct{})
	go s.reap()

	return nil
}

func (s *ManagedServer) Handshake() Handshake {
	return s.handshake
}

func (s *ManagedServer) Stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	log := s.logger()
	pid := s.cmd.Process.Pid

	select {
	case <-s.done:
		s.logUnexpectedExit(log, pid)
		return
	default:
	}

	log.Info("stopping plugin server", "pid", pid)
	pgid, err := syscall.Getpgid(pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-s.done:
		log.Info("plugin server exited cleanly", "pid", pid)
	case <-time.After(stopGracePeriod):
		log.Warn("plugin server did not exit within grace period; killing",
			"pid", pid, "grace", stopGracePeriod.String())
		if pgid, err := syscall.Getpgid(pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = s.cmd.Process.Kill()
		}
		<-s.done
	}
}

func (s *ManagedServer) Done() <-chan struct{} {
	return s.done
}

func (s *ManagedServer) reap() {
	_ = s.cmd.Wait()
	s.waitState = s.cmd.ProcessState
	close(s.done)
}

func (s *ManagedServer) logUnexpectedExit(log *slog.Logger, pid int) {
	attrs := []any{"pid", pid}
	if s.waitState != nil {
		attrs = append(attrs, "exit_code", s.waitState.ExitCode())
		if sig := exitSignal(s.waitState); sig != "" {
			attrs = append(attrs, "signal", sig)
		}
	}
	log.Error("plugin server died unexpectedly", attrs...)
}

func (s *ManagedServer) readHandshake(ctx context.Context, r io.Reader) (Handshake, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger().Error("handshake reader panicked", "panic", fmt.Sprintf("%v", rec))
				ch <- result{err: fmt.Errorf("server %s: handshake reader panicked: %v", s.Name, rec)}
			}
		}()
		scanner := bufio.NewScanner(r)
		if scanner.Scan() {
			ch <- result{line: scanner.Text()}
		} else {
			err := scanner.Err()
			if err == nil {
				err = fmt.Errorf("server %s: stdout closed before handshake", s.Name)
			}
			ch <- result{err: err}
		}
	}()

	select {
	case <-ctx.Done():
		return Handshake{}, fmt.Errorf("server %s: handshake timeout: %w", s.Name, ctx.Err())
	case res := <-ch:
		if res.err != nil {
			return Handshake{}, res.err
		}
		hs, err := ParseHandshake(res.line)
		if err != nil {
			return Handshake{}, fmt.Errorf("server %s: %w", s.Name, err)
		}
		return hs, nil
	}
}

func (s *ManagedServer) forwardStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	log := s.logger()
	for scanner.Scan() {
		log.Info("plugin stderr", "line", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Warn("plugin stderr reader exited with error", "err", err)
	} else {
		log.Info("plugin stderr reader exited")
	}
}

func (s *ManagedServer) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		if pgid, err := syscall.Getpgid(s.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = s.cmd.Process.Kill()
		}
		if s.done != nil {
			<-s.done
		} else {
			_ = s.cmd.Wait()
		}
	}
}

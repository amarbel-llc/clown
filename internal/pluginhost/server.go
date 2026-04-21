package pluginhost

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const stopGracePeriod = 5 * time.Second

type ManagedServer struct {
	Name      string
	Def       ServerDef
	PluginDir string

	cmd       *exec.Cmd
	handshake Handshake
}

func (s *ManagedServer) Start(ctx context.Context) error {
	cmdPath := s.Def.Command
	if !strings.HasPrefix(cmdPath, "/") {
		cmdPath = s.PluginDir + "/" + cmdPath
	}

	s.cmd = exec.CommandContext(ctx, cmdPath, s.Def.Args...)
	s.cmd.Dir = s.PluginDir
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

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("server %s: start: %w", s.Name, err)
	}

	go s.forwardStderr(stderr)

	hs, err := s.readHandshake(ctx, stdout)
	if err != nil {
		s.kill()
		return err
	}
	s.handshake = hs

	healthCtx, cancel := context.WithTimeout(ctx, s.Def.Healthcheck.Timeout.Duration)
	defer cancel()

	if err := WaitHealthy(healthCtx, hs.Address, s.Def.Healthcheck.Path, s.Def.Healthcheck.Interval.Duration); err != nil {
		s.kill()
		return fmt.Errorf("server %s: %w", s.Name, err)
	}

	return nil
}

func (s *ManagedServer) Handshake() Handshake {
	return s.handshake
}

func (s *ManagedServer) Stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(s.cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}

	done := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(stopGracePeriod):
		if pgid, err := syscall.Getpgid(s.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = s.cmd.Process.Kill()
		}
		<-done
	}
}

func (s *ManagedServer) readHandshake(ctx context.Context, r io.Reader) (Handshake, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
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
	prefix := "[" + s.Name + "] "
	for scanner.Scan() {
		fmt.Fprintln(os.Stderr, prefix+scanner.Text())
	}
}

func (s *ManagedServer) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		if pgid, err := syscall.Getpgid(s.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
	}
}

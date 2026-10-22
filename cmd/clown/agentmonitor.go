package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// runAgentMonitor is `clown agent-monitor SOCK`, the monitor claude runs inside
// an agent unit in place of a job-platform monitor that now runs in the
// frontend (FDR 0020 "frontend monitors"): it prints the lines the frontend's
// relay serves on SOCK, reconnecting when the connection drops. It never
// writes to the socket. It exits when its own stdout is gone.
func runAgentMonitor(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "clown agent-monitor: usage: clown agent-monitor SOCK")
		return 2
	}
	backoff := 100 * time.Millisecond
	for {
		c, err := net.Dial("unix", args[0])
		if err != nil {
			time.Sleep(backoff)
			backoff = min(backoff*2, 2*time.Second)
			continue
		}
		backoff = 100 * time.Millisecond
		if uc, ok := c.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
		_, err = io.Copy(os.Stdout, c)
		_ = c.Close()
		var werr *os.PathError
		if errors.As(err, &werr) && werr.Op == "write" {
			return 0
		}
		time.Sleep(backoff)
	}
}

// runAgentMonitorServe is `clown agent-monitor-serve --socket SOCK --name NAME
// -- COMMAND`: the frontend half of a monitor relay on its own, with no unit
// and so no caller check, until SIGTERM/SIGINT. It exists for the bats test of
// the relay against a real monitor (zz-tests_bats/agent_monitor.bats); clown's
// own launches run the relay in-process (startFrontendEndpoints).
func runAgentMonitorServe(args []string) int {
	var sock, name string
	for len(args) > 1 && args[0] != "--" {
		switch args[0] {
		case "--socket":
			sock = args[1]
		case "--name":
			name = args[1]
		default:
			fmt.Fprintf(os.Stderr, "clown agent-monitor-serve: unknown argument %q\n", args[0])
			return 2
		}
		args = args[2:]
	}
	if len(args) < 2 || args[0] != "--" || sock == "" || name == "" {
		fmt.Fprintln(os.Stderr, "clown agent-monitor-serve: usage: clown agent-monitor-serve --socket SOCK --name NAME -- COMMAND")
		return 2
	}
	relay := &agentscope.MonitorRelay{
		Name:     name,
		Command:  strings.Join(args[1:], " "),
		Env:      os.Environ(),
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
		ShortRun: time.Second,
	}
	if err := relay.Start(sock); err != nil {
		fmt.Fprintf(os.Stderr, "clown agent-monitor-serve: %v\n", err)
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	_ = relay.Close()
	return 0
}

// moveMonitorsToFrontend rewrites the synthesized job-platform plugin's
// monitors for an agent-scope launch: each command is recorded to run in the
// frontend, and claude is given `clown agent-monitor <socket>` instead, so
// `ringmaster monitor` and `troupe receive` never run inside the unit (FDR
// 0020). Sockets live under $XDG_RUNTIME_DIR/clown/monitors/<principal>/ and
// are bound into the unit one by one.
func moveMonitorsToFrontend(pluginDir, principal string) ([]agentscope.MonitorSpec, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		return nil, errors.New("XDG_RUNTIME_DIR is unset; nowhere to put the monitor sockets")
	}
	if principal == "" || strings.ContainsAny(principal, "/") {
		return nil, fmt.Errorf("principal %q cannot name a monitor socket directory", principal)
	}
	manifestPath := filepath.Join(pluginDir, ".claude-plugin", "plugin.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest jobMonitorPlugin
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	dir := filepath.Join(runtime, "clown", "monitors", principal)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var specs []agentscope.MonitorSpec
	for i, mon := range manifest.Monitors {
		sock := filepath.Join(dir, mon.Name+".sock")
		if len(sock) >= 108 {
			return nil, fmt.Errorf("monitor socket path %q exceeds the unix socket limit", sock)
		}
		specs = append(specs, agentscope.MonitorSpec{Name: mon.Name, Command: mon.Command, Socket: sock})
		manifest.Monitors[i].Command = clownExePath() + " agent-monitor " + sock
	}
	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return specs, os.WriteFile(manifestPath, out, 0o600)
}

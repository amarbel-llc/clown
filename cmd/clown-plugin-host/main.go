package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/amarbel-llc/clown/internal/pluginhost"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	pluginDirs, downstream := splitArgs(os.Args[1:])
	if len(downstream) == 0 {
		fmt.Fprintln(os.Stderr, "clown-plugin-host: missing downstream command after --")
		os.Exit(1)
	}

	host := &pluginhost.Host{PluginDirs: pluginDirs}
	discovered, err := host.Discover()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		os.Exit(1)
	}

	if len(discovered) == 0 {
		execDownstream(downstream)
		return // unreachable after exec
	}

	os.Exit(runManaged(host, discovered, downstream))
}

func runManaged(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer, downstream []string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Fprintf(os.Stderr, "clown-plugin-host: launching %d HTTP MCP server(s)\n", len(discovered))

	if err := host.StartAll(ctx, discovered); err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		return 1
	}
	defer host.Shutdown()

	mcpJSON, err := pluginhost.GenerateMCPConfig(host.ServerURLs())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: generating mcp config: %v\n", err)
		return 1
	}

	tmpFile, err := os.CreateTemp("", "clown-mcp-*.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: creating temp file: %v\n", err)
		return 1
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(mcpJSON); err != nil {
		tmpFile.Close()
		fmt.Fprintf(os.Stderr, "clown-plugin-host: writing mcp config: %v\n", err)
		return 1
	}
	tmpFile.Close()

	args := append([]string{downstream[0], "--mcp-config", tmpFile.Name()}, downstream[1:]...)

	binary, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		return 1
	}

	cmd := exec.Command(binary, args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		if cmd.Process != nil {
			cmd.Process.Signal(sig)
		}
	}()

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		return 1
	}
	return 0
}

func splitArgs(args []string) (pluginDirs []string, downstream []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			downstream = args[i+1:]
			return
		}
		if args[i] == "--plugin-dir" && i+1 < len(args) {
			pluginDirs = append(pluginDirs, args[i+1])
			i++
			continue
		}
		fmt.Fprintf(os.Stderr, "clown-plugin-host: unknown flag %q\n", args[i])
		os.Exit(1)
	}
	return
}

func execDownstream(args []string) {
	binary, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		os.Exit(1)
	}
	if err := syscall.Exec(binary, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: exec %s: %v\n", binary, err)
		os.Exit(1)
	}
}

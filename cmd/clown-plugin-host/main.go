package main

import (
	"context"
	"fmt"
	"log/slog"
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
	logger, logFile, logPath, err := pluginhost.OpenLog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: opening log: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	logger.Info("clown-plugin-host starting",
		"version", version,
		"commit", commit,
		"pid", os.Getpid(),
		"args", os.Args[1:],
		"log_path", logPath,
	)

	os.Exit(run(logger, logPath))
}

func run(logger *slog.Logger, logPath string) int {
	parsed, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		logger.Error("arg parsing failed", "err", err)
		return 1
	}
	if len(parsed.downstream) == 0 {
		fmt.Fprintln(os.Stderr, "clown-plugin-host: missing downstream command after --")
		logger.Error("missing downstream command after --")
		return 1
	}

	skipFailed := parsed.skipFailed || os.Getenv("CLOWN_SKIP_FAILED_PLUGINS") == "1"
	verbose := parsed.verbose
	if verbose {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: logging to %s\n", logPath)
	}
	logger.Info("parsed arguments",
		"plugin_dirs", parsed.pluginDirs,
		"skip_failed", skipFailed,
		"verbose", verbose,
		"downstream", parsed.downstream,
	)

	host := &pluginhost.Host{PluginDirs: parsed.pluginDirs, Logger: logger, Verbose: verbose}
	discovered, err := host.Discover()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		logger.Error("discovery failed", "err", err)
		return 1
	}

	if len(discovered) == 0 {
		logger.Info("no plugin servers discovered; exec'ing downstream directly", "downstream", parsed.downstream)
		execDownstream(parsed.downstream)
		return 0 // unreachable after exec
	}

	return runManaged(host, discovered, parsed.downstream, skipFailed, verbose, logger)
}

func runManaged(host *pluginhost.Host, discovered []pluginhost.DiscoveredServer, downstream []string, skipFailed bool, verbose bool, logger *slog.Logger) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if verbose {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: launching %d HTTP MCP server(s)\n", len(discovered))
	}
	logger.Info("launching plugin servers", "count", len(discovered))

	report := host.StartAll(ctx, discovered)
	for _, f := range report.Failed {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %s failed: %v\n", f.Server.Name(), f.Err)
		logger.Error("plugin server failed to start", "server", f.Server.Name(), "err", f.Err)
	}

	if len(report.Failed) > 0 {
		switch {
		case skipFailed:
			fmt.Fprintf(os.Stderr, "clown-plugin-host: skipping %d failed server(s) (--skip-failed)\n", len(report.Failed))
			logger.Info("continuing despite failures",
				"failed", len(report.Failed),
				"started", len(report.Started),
				"reason", "skip_failed")
		case pluginhost.IsInteractive():
			cont, err := pluginhost.ConfirmContinueWithFailures(report.Failed)
			if err != nil {
				fmt.Fprintf(os.Stderr, "clown-plugin-host: prompt aborted: %v\n", err)
				logger.Error("interactive prompt failed", "err", err)
				host.Shutdown()
				return 1
			}
			if !cont {
				logger.Info("user chose to abort after plugin failures",
					"failed", len(report.Failed),
					"started", len(report.Started))
				host.Shutdown()
				return 1
			}
			logger.Info("user chose to continue despite failures",
				"failed", len(report.Failed),
				"started", len(report.Started))
		default:
			fmt.Fprintln(os.Stderr, "clown-plugin-host: aborting; pass --skip-failed or set CLOWN_SKIP_FAILED_PLUGINS=1 to continue with healthy servers")
			logger.Error("aborting: plugin failures and not interactive",
				"failed", len(report.Failed),
				"started", len(report.Started))
			host.Shutdown()
			return 1
		}
	}

	if len(report.Started) == 0 {
		logger.Info("no plugin servers healthy; exec'ing downstream directly", "downstream", downstream)
		execDownstream(downstream)
		return 0 // unreachable after exec
	}
	defer host.Shutdown()

	mcpJSON, err := pluginhost.GenerateMCPConfig(host.ServerEntries())
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: generating mcp config: %v\n", err)
		logger.Error("generating mcp config failed", "err", err)
		return 1
	}

	tmpFile, err := os.CreateTemp("", "clown-mcp-*.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: creating temp file: %v\n", err)
		logger.Error("creating mcp config temp file failed", "err", err)
		return 1
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(mcpJSON); err != nil {
		tmpFile.Close()
		fmt.Fprintf(os.Stderr, "clown-plugin-host: writing mcp config: %v\n", err)
		logger.Error("writing mcp config failed", "err", err)
		return 1
	}
	tmpFile.Close()
	logger.Info("wrote mcp config", "path", tmpFile.Name())

	args := append([]string{downstream[0], "--mcp-config", tmpFile.Name()}, downstream[1:]...)

	binary, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		logger.Error("locating downstream binary failed", "binary", args[0], "err", err)
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
		logger.Info("signal received; forwarding to downstream", "signal", sig.String())
		if cmd.Process != nil {
			cmd.Process.Signal(sig)
		}
	}()

	logger.Info("running downstream", "binary", binary, "args", args[1:])
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			logger.Info("downstream exited", "code", exitErr.ExitCode())
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		logger.Error("downstream run failed", "err", err)
		return 1
	}
	logger.Info("downstream exited", "code", 0)
	return 0
}

type parsedArgs struct {
	pluginDirs []string
	downstream []string
	skipFailed bool
	verbose    bool
}

func parseArgs(args []string) (parsedArgs, error) {
	var p parsedArgs
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			p.downstream = args[i+1:]
			return p, nil
		case args[i] == "--plugin-dir":
			if i+1 >= len(args) {
				return p, fmt.Errorf("--plugin-dir requires an argument")
			}
			p.pluginDirs = append(p.pluginDirs, args[i+1])
			i++
		case args[i] == "--skip-failed":
			p.skipFailed = true
		case args[i] == "--verbose", args[i] == "-v":
			p.verbose = true
		default:
			return p, fmt.Errorf("unknown flag %q", args[i])
		}
	}
	return p, nil
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

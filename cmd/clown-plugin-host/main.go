package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/amarbel-llc/clown/internal/buildcfg"
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
	disableClown := parsed.disableClownProtocol || os.Getenv("CLOWN_DISABLE_CLOWN_PROTOCOL") == "1"
	verbose := parsed.verbose
	if verbose {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: logging to %s\n", logPath)
	}
	logger.Info("parsed arguments",
		"plugin_dirs", parsed.pluginDirs,
		"skip_failed", skipFailed,
		"disable_clown_protocol", disableClown,
		"verbose", verbose,
		"downstream", parsed.downstream,
	)

	if disableClown {
		logger.Info("clown protocol disabled; passing plugin dirs through to downstream unchanged")
		execDownstream(buildDownstreamArgs(parsed.downstream, parsed.pluginDirs, nil))
		return 0 // unreachable after exec
	}

	host := &pluginhost.Host{
		PluginDirs: parsed.pluginDirs,
		Logger:     logger,
		Verbose:    verbose,
		BridgePath: buildcfg.StdioBridgePath,
	}
	discovered, err := host.Discover()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: %v\n", err)
		logger.Error("discovery failed", "err", err)
		return 1
	}

	if len(discovered) == 0 {
		logger.Info("no plugin servers discovered; passing plugin dirs through to downstream", "downstream", parsed.downstream)
		execDownstream(buildDownstreamArgs(parsed.downstream, parsed.pluginDirs, nil))
		return 0 // unreachable after exec
	}

	return runManaged(host, discovered, parsed.downstream, parsed.pluginDirs, skipFailed, verbose, logger)
}

func runManaged(
	host *pluginhost.Host,
	discovered []pluginhost.DiscoveredServer,
	downstream []string,
	pluginDirs []string,
	skipFailed bool,
	verbose bool,
	logger *slog.Logger,
) int {
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
		logger.Info("no plugin servers healthy; falling back to original plugin dirs so claude's native MCP still works")
		host.Shutdown()
		execDownstream(buildDownstreamArgs(downstream, pluginDirs, nil))
		return 0 // unreachable after exec
	}
	defer host.Shutdown()

	dirMap, err := host.CompileForClaude(discovered)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown-plugin-host: compiling plugin manifests: %v\n", err)
		logger.Error("compiling plugin manifests failed", "err", err)
		return 1
	}

	args := buildDownstreamArgs(downstream, pluginDirs, dirMap)

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

// buildDownstreamArgs assembles the argv that exec's the downstream (claude).
// It prepends one --plugin-dir per entry in pluginDirs (substituting the
// compiled path from dirMap if available). Order: [binary, --plugin-dir dir1,
// --plugin-dir dir2, ..., <original downstream args>].
func buildDownstreamArgs(downstream []string, pluginDirs []string, dirMap map[string]string) []string {
	args := []string{downstream[0]}
	for _, dir := range pluginDirs {
		target := dir
		if staged, ok := dirMap[dir]; ok {
			target = staged
		}
		args = append(args, "--plugin-dir", target)
	}
	return append(args, downstream[1:]...)
}

type parsedArgs struct {
	pluginDirs           []string
	downstream           []string
	skipFailed           bool
	disableClownProtocol bool
	verbose              bool
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
		case args[i] == "--disable-clown-protocol":
			p.disableClownProtocol = true
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

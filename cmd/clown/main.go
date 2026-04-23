package main

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"golang.org/x/term"

	"github.com/amarbel-llc/clown/internal/buildcfg"
	"github.com/amarbel-llc/clown/internal/pluginhost"
	"github.com/amarbel-llc/clown/internal/profile"
	"github.com/amarbel-llc/clown/internal/promptwalk"
	"github.com/amarbel-llc/clown/internal/provider"
)

//go:embed profiles/builtin.toml
var builtinProfilesTOML []byte

func loadProfiles(additionalPath string) ([]profile.Profile, error) {
	var f struct {
		Profile []profile.Profile `toml:"profile"`
	}
	if _, err := toml.Decode(string(builtinProfilesTOML), &f); err != nil {
		return nil, fmt.Errorf("builtin profiles: %w", err)
	}
	builtin := f.Profile

	if additionalPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return builtin, nil
		}
		additionalPath = filepath.Join(home, ".config", "circus", "profiles.toml")
	}

	additional, err := profile.Load(additionalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return builtin, nil
		}
		return nil, fmt.Errorf("additional profiles: %w", err)
	}
	return profile.Merge(builtin, additional), nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(rawArgs []string) int {
	flags, err := parseFlags(rawArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	if flags.version {
		printVersion()
		return 0
	}

	profiles, err := loadProfiles("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: loading profiles: %v\n", err)
		return 1
	}

	var selectedProfile *profile.Profile
	if flags.profile != "" {
		for i, p := range profiles {
			if p.Name == flags.profile {
				selectedProfile = &profiles[i]
				break
			}
		}
		if selectedProfile == nil {
			fmt.Fprintf(os.Stderr, "clown: unknown profile %q\n", flags.profile)
			fmt.Fprintln(os.Stderr, "available profiles:")
			for _, p := range profiles {
				fmt.Fprintf(os.Stderr, "  %-20s %s\n", p.Name, p.Display)
			}
			return 1
		}
		if err := profile.Validate(*selectedProfile); err != nil {
			fmt.Fprintf(os.Stderr, "clown: invalid profile: %v\n", err)
			return 1
		}
		flags.provider = selectedProfile.Provider
	}

	cliPath, err := resolveProvider(flags.provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	if flags.naked {
		if flags.provider == "opencode" {
			fmt.Fprintln(os.Stderr, "clown: --naked is not supported with --provider opencode (config injection required)")
			return 1
		}
		execProcess(cliPath, flags.forwarded)
		return 0 // unreachable
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	prompts, err := promptwalk.WalkPrompts(cwd, homeDir, buildcfg.SystemPromptAppendD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: collecting prompts: %v\n", err)
		return 1
	}

	pluginDirs := readPluginDirs()

	switch flags.provider {
	case "claude":
		return runClaude(cliPath, flags, prompts, pluginDirs)
	case "codex":
		return runCodex(cliPath, flags, prompts)
	case "circus":
		return runCircus(cliPath, flags, prompts, pluginDirs)
	case "opencode":
		return runOpencode(cliPath, flags.forwarded, selectedProfile)
	default:
		fmt.Fprintf(os.Stderr, "clown: unknown provider %q\n", flags.provider)
		return 1
	}
}

func runClaude(cliPath string, flags parsedFlags, prompts promptwalk.PromptResult, pluginDirs []string) int {
	args, cleanup, err := provider.BuildClaudeArgs(provider.ClaudeArgs{
		CLIPath:             cliPath,
		AgentsFile:          buildcfg.AgentsFile,
		DisallowedToolsFile: buildcfg.DisallowedToolsFile,
		SystemPromptFile:    prompts.SystemPromptFile,
		AppendFragments:     prompts.AppendFragments,
	}, flags.forwarded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: building claude args: %v\n", err)
		return 1
	}
	defer cleanup()

	skipFailed := flags.skipFailed || os.Getenv("CLOWN_SKIP_FAILED_PLUGINS") == "1"
	disableClown := flags.disableClownProtocol || os.Getenv("CLOWN_DISABLE_CLOWN_PROTOCOL") == "1"
	verbose := flags.verbose

	if disableClown {
		fullArgs := prependPluginDirs(args, pluginDirs, nil)
		execProcess(cliPath, fullArgs)
		return 0 // unreachable
	}

	logger, logFile, logPath, err := pluginhost.OpenLog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: opening log: %v\n", err)
		return 1
	}
	defer logFile.Close()
	if verbose {
		fmt.Fprintf(os.Stderr, "clown: logging to %s\n", logPath)
	}

	logger.Info("clown starting",
		"version", buildcfg.Version,
		"commit", buildcfg.Commit,
		"pid", os.Getpid(),
		"log_path", logPath,
	)

	host := &pluginhost.Host{PluginDirs: pluginDirs, Logger: logger, Verbose: verbose}
	discovered, err := host.Discover()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		logger.Error("discovery failed", "err", err)
		return 1
	}

	if len(discovered) == 0 {
		logger.Info("no plugin servers discovered; passing plugin dirs through")
		fullArgs := prependPluginDirs(args, pluginDirs, nil)
		execProcess(cliPath, fullArgs)
		return 0 // unreachable
	}

	return runManaged(host, discovered, cliPath, args, pluginDirs, skipFailed, verbose, logger)
}

func runManaged(
	host *pluginhost.Host,
	discovered []pluginhost.DiscoveredServer,
	cliPath string,
	baseArgs []string,
	pluginDirs []string,
	skipFailed bool,
	verbose bool,
	logger *slog.Logger,
) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if verbose {
		fmt.Fprintf(os.Stderr, "clown: launching %d HTTP MCP server(s)\n", len(discovered))
	}
	logger.Info("launching plugin servers", "count", len(discovered))

	report := host.StartAll(ctx, discovered)
	for _, f := range report.Failed {
		fmt.Fprintf(os.Stderr, "clown: %s failed: %v\n", f.Server.Name(), f.Err)
		logger.Error("plugin server failed to start", "server", f.Server.Name(), "err", f.Err)
	}

	if len(report.Failed) > 0 {
		switch {
		case skipFailed:
			fmt.Fprintf(os.Stderr, "clown: skipping %d failed server(s) (--skip-failed)\n", len(report.Failed))
			logger.Info("continuing despite failures",
				"failed", len(report.Failed),
				"started", len(report.Started),
				"reason", "skip_failed")
		case pluginhost.IsInteractive():
			cont, err := pluginhost.ConfirmContinueWithFailures(report.Failed)
			if err != nil {
				fmt.Fprintf(os.Stderr, "clown: prompt aborted: %v\n", err)
				logger.Error("interactive prompt failed", "err", err)
				host.Shutdown()
				return 1
			}
			if !cont {
				logger.Info("user chose to abort after plugin failures")
				host.Shutdown()
				return 1
			}
			logger.Info("user chose to continue despite failures")
		default:
			fmt.Fprintln(os.Stderr, "clown: aborting; pass --skip-failed or set CLOWN_SKIP_FAILED_PLUGINS=1 to continue with healthy servers")
			logger.Error("aborting: plugin failures and not interactive")
			host.Shutdown()
			return 1
		}
	}

	if len(report.Started) == 0 {
		logger.Info("no plugin servers healthy; falling back to original plugin dirs")
		host.Shutdown()
		fullArgs := prependPluginDirs(baseArgs, pluginDirs, nil)
		execProcess(cliPath, fullArgs)
		return 0 // unreachable
	}
	defer host.Shutdown()

	dirMap, err := host.CompileForClaude(discovered)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: compiling plugin manifests: %v\n", err)
		logger.Error("compiling plugin manifests failed", "err", err)
		return 1
	}

	fullArgs := prependPluginDirs(baseArgs, pluginDirs, dirMap)

	binary, err := exec.LookPath(cliPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		logger.Error("locating provider binary failed", "binary", cliPath, "err", err)
		return 1
	}

	cmd := exec.Command(binary, fullArgs...)
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

	logger.Info("running downstream", "binary", binary, "args", fullArgs)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			logger.Info("downstream exited", "code", code)
			resetTerminal()
			return code
		}
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		logger.Error("downstream run failed", "err", err)
		return 1
	}
	logger.Info("downstream exited", "code", 0)
	resetTerminal()
	return 0
}

func runCodex(cliPath string, flags parsedFlags, prompts promptwalk.PromptResult) int {
	args, cleanup, err := provider.BuildCodexArgs(provider.CodexArgs{
		CLIPath:          cliPath,
		SystemPromptFile: prompts.SystemPromptFile,
		AppendFragments:  prompts.AppendFragments,
	}, flags.forwarded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: building codex args: %v\n", err)
		return 1
	}
	defer cleanup()

	execProcess(cliPath, args)
	return 0 // unreachable
}

func runCircus(circusPath string, flags parsedFlags, prompts promptwalk.PromptResult, pluginDirs []string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Resolve the model: --model from CLI takes priority, then the build default.
	forwarded := flags.forwarded
	modelName := flagValue(forwarded, "--model")
	if modelName == "" {
		modelName = buildcfg.CircusModelName
	}
	if modelName != "" && !hasFlag(forwarded, "--model") {
		forwarded = append([]string{"--model", modelName}, forwarded...)
	}

	// Start circus with the selected model.
	circusArgs := []string{"start"}
	if modelName != "" {
		circusArgs = append(circusArgs, "--model", modelName)
	}
	cmd := exec.CommandContext(ctx, circusPath, circusArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: circus stdin pipe: %v\n", err)
		return 1
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: circus stdout pipe: %v\n", err)
		return 1
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "clown: starting circus: %v\n", err)
		return 1
	}
	defer func() {
		stdinPipe.Close()
		cmd.Wait()
	}()

	hs, err := readCircusHandshake(stdoutPipe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: circus handshake: %v\n", err)
		return 1
	}

	baseURL := "http://" + hs.Address

	claudePath := buildcfg.ClaudeCliPath
	args, cleanup, err := provider.BuildClaudeArgs(provider.ClaudeArgs{
		CLIPath:             claudePath,
		AgentsFile:          buildcfg.AgentsFile,
		DisallowedToolsFile: buildcfg.DisallowedToolsFile,
		SystemPromptFile:    prompts.SystemPromptFile,
		AppendFragments:     prompts.AppendFragments,
	}, forwarded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: building claude args: %v\n", err)
		return 1
	}
	defer cleanup()

	fullArgs := prependPluginDirs(args, pluginDirs, nil)

	binary, err := exec.LookPath(claudePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	claudeCmd := exec.Command(binary, fullArgs...)
	claudeCmd.Stdin = os.Stdin
	claudeCmd.Stdout = os.Stdout
	claudeCmd.Stderr = os.Stderr
	circusEnv := []string{"ANTHROPIC_BASE_URL=" + baseURL}
	if modelName != "" {
		circusEnv = append(circusEnv, "ANTHROPIC_CUSTOM_MODEL_OPTION="+modelName)
	}
	claudeCmd.Env = append(os.Environ(), circusEnv...)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		if claudeCmd.Process != nil {
			claudeCmd.Process.Signal(sig)
		}
	}()

	if err := claudeCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			resetTerminal()
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}
	resetTerminal()
	return 0
}

func readCircusHandshake(r io.Reader) (pluginhost.Handshake, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return pluginhost.Handshake{}, err
		}
		return pluginhost.Handshake{}, fmt.Errorf("circus closed stdout before handshake")
	}
	return pluginhost.ParseHandshake(scanner.Text())
}

// prependPluginDirs inserts --plugin-dir flags at the start of args,
// substituting compiled paths from dirMap where available.
func prependPluginDirs(args []string, pluginDirs []string, dirMap map[string]string) []string {
	var result []string
	for _, dir := range pluginDirs {
		target := dir
		if staged, ok := dirMap[dir]; ok {
			target = staged
		}
		result = append(result, "--plugin-dir", target)
	}
	return append(result, args...)
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

func resolveProvider(name string) (string, error) {
	switch name {
	case "claude":
		return buildcfg.ClaudeCliPath, nil
	case "codex":
		return buildcfg.CodexCliPath, nil
	case "circus":
		return buildcfg.CircusCliPath, nil
	case "opencode":
		return buildcfg.OpencodeCliPath, nil
	default:
		return "", fmt.Errorf("unknown provider %q", name)
	}
}

func readPluginDirs() []string {
	metaDir := os.Getenv("CLOWN_PLUGIN_META")
	if metaDir == "" {
		return nil
	}
	path := metaDir + "/plugin-dirs"
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var dirs []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			dirs = append(dirs, line)
		}
	}
	return dirs
}

func printVersion() {
	type row struct {
		component string
		version   string
		rev       string
	}

	header := row{"COMPONENT", "VERSION", "REV"}
	fixed := []row{
		{"claude-code", buildcfg.ClaudeCodeVersion, buildcfg.ClaudeCodeRev},
		{"clown", buildcfg.Version, buildcfg.Commit},
		{"codex", buildcfg.CodexVersion, buildcfg.CodexRev},
	}

	var plugin []row
	metaDir := os.Getenv("CLOWN_PLUGIN_META")
	if metaDir != "" {
		if data, err := os.ReadFile(metaDir + "/version-info"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					plugin = append(plugin, row{component: line})
				}
			}
		}
	}

	all := append(fixed, plugin...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].component < all[j].component
	})

	fmt.Fprintf(os.Stdout, "%-20s %-12s %s\n", header.component, header.version, header.rev)
	for _, r := range all {
		if r.version != "" {
			fmt.Fprintf(os.Stdout, "%-20s %-12s %s\n", r.component, r.version, r.rev)
		} else {
			fmt.Fprintln(os.Stdout, r.component)
		}
	}
}

// resetTerminal emits escape sequences to restore a sane terminal state
// after claude-code exits. Only emits if stderr is a terminal.
func resetTerminal() {
	if term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprint(os.Stderr, "\033[?2004l\033[?1l\033[?25h\033[J")
	}
}

func execProcess(binary string, args []string) {
	resolved, err := exec.LookPath(binary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		os.Exit(1)
	}
	argv := append([]string{resolved}, args...)
	if err := syscall.Exec(resolved, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "clown: exec %s: %v\n", binary, err)
		os.Exit(1)
	}
}

type parsedFlags struct {
	provider             string
	profile              string
	naked                bool
	skipFailed           bool
	disableClownProtocol bool
	verbose              bool
	version              bool
	forwarded            []string
}

func parseFlags(args []string) (parsedFlags, error) {
	p := parsedFlags{
		provider: os.Getenv("CLOWN_PROVIDER"),
	}
	if p.provider == "" {
		p.provider = "claude"
	}
	p.profile = os.Getenv("CLOWN_PROFILE")

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "version" && i == 0:
			p.version = true
			return p, nil
		case args[i] == "--provider":
			if i+1 >= len(args) {
				return p, fmt.Errorf("--provider requires an argument")
			}
			p.provider = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--provider="):
			p.provider = strings.TrimPrefix(args[i], "--provider=")
		case args[i] == "--profile":
			if i+1 >= len(args) {
				return p, fmt.Errorf("--profile requires an argument")
			}
			p.profile = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--profile="):
			p.profile = strings.TrimPrefix(args[i], "--profile=")
		case args[i] == "--naked":
			p.naked = true
		case args[i] == "--skip-failed":
			p.skipFailed = true
		case args[i] == "--disable-clown-protocol":
			p.disableClownProtocol = true
		case args[i] == "--verbose" || args[i] == "-v":
			p.verbose = true
		default:
			p.forwarded = append(p.forwarded, args[i:]...)
			return p, nil
		}
	}
	return p, nil
}

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
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
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

type profileItem struct{ p profile.Profile }

func (i profileItem) Title() string       { return i.p.Display }
func (i profileItem) Description() string { return i.p.Provider + " / " + i.p.Backend }
func (i profileItem) FilterValue() string { return i.p.Name + " " + i.p.Display }

type pickerModel struct {
	list   list.Model
	chosen *profile.Profile
	quit   bool
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if i, ok := m.list.SelectedItem().(profileItem); ok {
				p := i.p
				m.chosen = &p
			}
			return m, tea.Quit
		case "q", "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width)
		m.list.SetHeight(msg.Height - 2)
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string { return m.list.View() }

func pickProfile(profiles []profile.Profile) (*profile.Profile, error) {
	items := make([]list.Item, len(profiles))
	for i, p := range profiles {
		items[i] = profileItem{p}
	}
	l := list.New(items, list.NewDefaultDelegate(), 40, 14)
	l.Title = "Select a profile"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	m, err := tea.NewProgram(pickerModel{list: l}, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	pm := m.(pickerModel)
	if pm.quit {
		return nil, nil
	}
	return pm.chosen, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(rawArgs []string) int {
	if len(rawArgs) > 0 {
		switch rawArgs[0] {
		case "resume":
			return runResume(rawArgs[1:])
		case "sessions-complete":
			return runSessionsComplete(rawArgs[1:])
		}
	}

	flags, err := parseFlags(rawArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		return 1
	}

	if flags.version {
		printVersion()
		return 0
	}

	if flags.help {
		printHelp()
		return 0
	}

	return runWithFlags(flags)
}

// runWithFlags executes the main provider-dispatch pipeline. Split out
// from run() so subcommands like `resume` can construct a parsedFlags
// directly and rejoin the standard flow (profile load, prompt walk,
// plugin host, provider exec) without re-running parseFlags.
func runWithFlags(flags parsedFlags) int {
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

	if selectedProfile == nil && !flags.version && !flags.naked && !flags.providerExplicit && term.IsTerminal(int(os.Stdin.Fd())) {
		chosen, err := pickProfile(profiles)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown: profile picker: %v\n", err)
			return 1
		}
		if chosen == nil {
			return 0
		}
		selectedProfile = chosen
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

	pluginDirs := readPluginDirs()
	pluginDirs = append(pluginDirs, flags.extraPluginDirs...)

	// Per FDR 0003, plugin-contributed system-prompt-append.d
	// fragments are layered between clown's builtin fragments and
	// the user's .circus/system-prompt.d/ fragments.
	builtinAppendDirs := append([]string{buildcfg.SystemPromptAppendD}, readPluginFragmentDirs()...)

	prompts, err := promptwalk.WalkPrompts(cwd, homeDir, builtinAppendDirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: collecting prompts: %v\n", err)
		return 1
	}

	switch flags.provider {
	case "claude":
		return runClaude(cliPath, flags, prompts, pluginDirs)
	case "codex":
		return runCodex(cliPath, flags, prompts)
	case "circus":
		return runCircus(cliPath, flags, prompts, pluginDirs)
	case "opencode":
		return runOpencode(cliPath, flags.forwarded, selectedProfile)
	case "clownbox":
		return runClownbox(cliPath, flags, prompts, pluginDirs)
	default:
		fmt.Fprintf(os.Stderr, "clown: unknown provider %q\n", flags.provider)
		return 1
	}
}

// clownboxDisabledMessage is returned by resolveProvider when the
// clownbox provider is requested in a build that omits its closure.
// The build-time ldflag leaves ClownboxCliPath as the empty string when
// clownbox is disabled; revive it by restoring the Nix derivation chain
// and the ldflag (see flake.nix).
const clownboxDisabledMessage = "clownbox provider is disabled in this build"

// Executor abstracts how a provider receives its argv. The plugin-host
// pipeline is identical for claude and clownbox; only the final exec
// differs. claude takes args directly; clownbox prepends `--` so its
// arg parser stops and forwards the rest verbatim to the inner claude.
type Executor interface {
	// Binary resolves the absolute path of the executable to run.
	Binary() (string, error)
	// FormatArgs transforms the post-prependPluginDirs argv into the
	// argv that the executable should actually receive (excluding argv[0]).
	FormatArgs(fullArgs []string) []string
}

// directExecutor passes args through unchanged. Used by the claude
// provider: claude takes --plugin-dir, --system-prompt-file, etc.
// directly.
type directExecutor struct{ cliPath string }

func (e *directExecutor) Binary() (string, error)           { return exec.LookPath(e.cliPath) }
func (e *directExecutor) FormatArgs(args []string) []string { return args }

// passthroughExecutor prepends `--` so a wrapper's arg parser stops
// and the remaining args reach the wrapped binary verbatim. Used by
// the clownbox provider: claudebox accepts `claudebox -- <claude-args>`
// (per nix/patches/claudebox-arg-passthrough.patch).
type passthroughExecutor struct{ cliPath string }

func (e *passthroughExecutor) Binary() (string, error) { return exec.LookPath(e.cliPath) }
func (e *passthroughExecutor) FormatArgs(args []string) []string {
	return append([]string{"--"}, args...)
}

func runClaude(cliPath string, flags parsedFlags, prompts promptwalk.PromptResult, pluginDirs []string) int {
	return withClaudeResumeHint(flags.forwarded, func(forwarded []string) int {
		args, cleanup, err := provider.BuildClaudeArgs(provider.ClaudeArgs{
			CLIPath:             cliPath,
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

		return runWithPluginHost(&directExecutor{cliPath: cliPath}, args, pluginDirs, flags)
	})
}

// runWithPluginHost runs a provider through clown's plugin-host
// pipeline: discover plugin servers, spawn HTTP MCPs, compile manifests
// pointing at the running servers, and run the provider with the
// staged plugin dirs. Falls back to running the provider directly when
// there are no plugins to manage or when --disable-clown-protocol is
// set. The Executor parameter is what makes this work for both claude
// (direct) and clownbox (passthrough); everything else is
// provider-agnostic.
//
// All paths run the provider as a subprocess (cmd.Run) rather than
// syscall.Exec, so clown retains control after the provider exits and
// can run post-exit hooks like the resume hint.
func runWithPluginHost(executor Executor, args []string, pluginDirs []string, flags parsedFlags) int {
	skipFailed := flags.skipFailed || os.Getenv("CLOWN_SKIP_FAILED_PLUGINS") == "1"
	disableClown := flags.disableClownProtocol || os.Getenv("CLOWN_DISABLE_CLOWN_PROTOCOL") == "1"
	verbose := flags.verbose

	if disableClown {
		fullArgs := prependPluginDirs(args, pluginDirs, nil)
		return runProvider(executor, fullArgs, nil)
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
		"plugin_dirs", pluginDirs,
		"plugin_meta_env", os.Getenv("CLOWN_PLUGIN_META"),
		"bridge_path", buildcfg.StdioBridgePath,
	)

	host := &pluginhost.Host{
		PluginDirs: pluginDirs,
		Logger:     logger,
		BridgePath: buildcfg.StdioBridgePath,
	}
	discovered, err := host.Discover()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		logger.Error("discovery failed", "err", err)
		return 1
	}

	if len(discovered) == 0 {
		logger.Info("no plugin servers discovered; passing plugin dirs through")
		fullArgs := prependPluginDirs(args, pluginDirs, nil)
		return runProvider(executor, fullArgs, logger)
	}

	return runManaged(host, discovered, executor, args, pluginDirs, skipFailed, verbose, logger)
}

// runProvider executes a provider as a subprocess, forwarding stdio
// and signals. Returns the provider's exit code (or 1 on a clown-side
// failure). Used by every non-naked path so clown stays in the
// process tree and can run post-exit hooks.
func runProvider(executor Executor, args []string, logger *slog.Logger) int {
	binary, err := executor.Binary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		if logger != nil {
			logger.Error("locating provider binary failed", "err", err)
		}
		return 1
	}

	argv := executor.FormatArgs(args)
	cmd := exec.Command(binary, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		if logger != nil {
			logger.Info("signal received; forwarding to downstream", "signal", sig.String())
		}
		if cmd.Process != nil {
			cmd.Process.Signal(sig)
		}
	}()

	if logger != nil {
		logger.Info("running downstream", "binary", binary, "args", argv)
	}
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if logger != nil {
				logger.Info("downstream exited", "code", code)
			}
			resetTerminal()
			return code
		}
		fmt.Fprintf(os.Stderr, "clown: %v\n", err)
		if logger != nil {
			logger.Error("downstream run failed", "err", err)
		}
		return 1
	}
	if logger != nil {
		logger.Info("downstream exited", "code", 0)
	}
	resetTerminal()
	return 0
}

func runManaged(
	host *pluginhost.Host,
	discovered []pluginhost.DiscoveredServer,
	executor Executor,
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
		return runProvider(executor, fullArgs, logger)
	}
	defer host.Shutdown()

	dirMap, err := host.CompileForClaude(discovered)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: compiling plugin manifests: %v\n", err)
		logger.Error("compiling plugin manifests failed", "err", err)
		return 1
	}

	fullArgs := prependPluginDirs(baseArgs, pluginDirs, dirMap)
	return runProvider(executor, fullArgs, logger)
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
	// Resolve the model: --model from CLI takes priority, then the build default.
	forwarded := flags.forwarded
	modelName := flagValue(forwarded, "--model")
	if modelName == "" {
		modelName = buildcfg.CircusModelName
	}
	if modelName != "" && !hasFlag(forwarded, "--model") {
		forwarded = append([]string{"--model", modelName}, forwarded...)
	}

	return withClaudeResumeHint(forwarded, func(forwarded []string) int {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

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
	})
}

// runClownbox launches claude-code wrapped in the clownbox sandbox (a fork
// of numtide/claudebox patched for `--` arg passthrough). The sandbox
// shadows $HOME with an isolated session dir and mounts the repo
// writable; /tmp inside the sandbox is a fresh tmpfs, so any prompt-
// fragment temp files written by BuildClaudeArgs must land inside the
// repo bind-mount. We point TMPDIR at <repoRoot>/.tmp/ for the duration
// of arg-building.
//
// Plugin-host orchestration is handled by runWithPluginHost using the
// passthroughExecutor — clownbox's bwrap profile uses --share-net, so
// HTTP MCP servers spawned on the host's localhost are reachable from
// inside the sandbox without further plumbing.
func runClownbox(cliPath string, flags parsedFlags, prompts promptwalk.PromptResult, pluginDirs []string) int {
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: getwd: %v\n", err)
		return 1
	}
	stagingDir := filepath.Join(repoRoot, ".tmp")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "clown: creating staging dir %s: %v\n", stagingDir, err)
		return 1
	}
	prevTmp, hadTmp := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", stagingDir); err != nil {
		fmt.Fprintf(os.Stderr, "clown: setting TMPDIR: %v\n", err)
		return 1
	}
	defer func() {
		if hadTmp {
			os.Setenv("TMPDIR", prevTmp)
		} else {
			os.Unsetenv("TMPDIR")
		}
	}()

	args, cleanup, err := provider.BuildClaudeArgs(provider.ClaudeArgs{
		CLIPath:             cliPath,
		AgentsFile:          buildcfg.AgentsFile,
		DisallowedToolsFile: buildcfg.DisallowedToolsFile,
		SystemPromptFile:    prompts.SystemPromptFile,
		AppendFragments:     prompts.AppendFragments,
	}, flags.forwarded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: building clownbox args: %v\n", err)
		return 1
	}
	defer cleanup()

	return runWithPluginHost(&passthroughExecutor{cliPath: cliPath}, args, pluginDirs, flags)
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
	case "clownbox":
		if buildcfg.ClownboxCliPath == "" {
			return "", fmt.Errorf("%s", clownboxDisabledMessage)
		}
		return buildcfg.ClownboxCliPath, nil
	default:
		return "", fmt.Errorf("unknown provider %q", name)
	}
}

func readPluginDirs() []string {
	return readMetaList("plugin-dirs")
}

// readPluginFragmentDirs returns the absolute paths to plugin-shipped
// .clown-plugin/system-prompt-append.d/ directories, in plugin-list
// order. Per FDR 0003, mkCircus's resolvePlugins step writes these to
// `$CLOWN_PLUGIN_META/plugin-fragment-dirs`; clown reads them at
// runtime and layers them between builtin and user fragments.
func readPluginFragmentDirs() []string {
	return readMetaList("plugin-fragment-dirs")
}

// readMetaList reads a newline-delimited list from a file under
// $CLOWN_PLUGIN_META, skipping blanks. Missing env var or missing file
// yields nil — both are normal: a clown without plugins has no meta
// dir, and pre-FDR-0003 builds may not yet emit
// plugin-fragment-dirs.
func readMetaList(name string) []string {
	metaDir := os.Getenv("CLOWN_PLUGIN_META")
	if metaDir == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(metaDir, name))
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			entries = append(entries, line)
		}
	}
	return entries
}

func printHelp() {
	defaultProvider := buildcfg.DefaultProvider
	if defaultProvider == "" {
		defaultProvider = "claude"
	}
	defaultProfileSuffix := ""
	if buildcfg.DefaultProfile != "" {
		defaultProfileSuffix = fmt.Sprintf(" (default: %s)", buildcfg.DefaultProfile)
	}
	fmt.Printf(`Usage: clown [clown-flags] -- [provider-args]

Clown flags (must appear before --):
  --provider <name>          Provider to use: claude, codex, circus, opencode (default: %s)
  --profile <name>           Profile name; implies --provider from profile config%s
  --naked                    Pass through to provider without clown wrapping
  --skip-failed              Continue if plugin servers fail to start
  --disable-clown-protocol   Disable clown plugin-host protocol
  --verbose, -v              Enable verbose output
  --help, -h                 Show this help text
  version                    Print version information (first argument only)
  resume                     Pick a resumable session in $PWD (claude only)
  sessions-complete          Emit fish-completion lines for sessions

All arguments after -- are forwarded verbatim to the provider.
`, defaultProvider, defaultProfileSuffix)
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
	providerExplicit     bool
	profile              string
	naked                bool
	skipFailed           bool
	disableClownProtocol bool
	verbose              bool
	version              bool
	help                 bool
	forwarded            []string
	// extraPluginDirs holds plugin directories supplied at the command
	// line via --plugin-dir. They are appended to the baked-in set from
	// CLOWN_PLUGIN_META and let users wire ad-hoc plugins (typically
	// stdioServers test plugins) without re-baking the build.
	extraPluginDirs []string
}

func parseFlags(args []string) (parsedFlags, error) {
	p := parsedFlags{}
	if env := os.Getenv("CLOWN_PROVIDER"); env != "" {
		p.provider = env
		p.providerExplicit = true
	} else if buildcfg.DefaultProvider != "" {
		p.provider = buildcfg.DefaultProvider
	} else {
		p.provider = "claude"
	}
	p.profile = os.Getenv("CLOWN_PROFILE")

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			if i+1 < len(args) {
				p.forwarded = args[i+1:]
			}
			return p, nil
		case args[i] == "version" && i == 0:
			p.version = true
			return p, nil
		case args[i] == "--help" || args[i] == "-h":
			p.help = true
			return p, nil
		case args[i] == "--provider":
			if i+1 >= len(args) {
				return p, fmt.Errorf("--provider requires an argument")
			}
			p.provider = args[i+1]
			p.providerExplicit = true
			i++
		case strings.HasPrefix(args[i], "--provider="):
			p.provider = strings.TrimPrefix(args[i], "--provider=")
			p.providerExplicit = true
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
		case args[i] == "--plugin-dir":
			if i+1 >= len(args) {
				return p, fmt.Errorf("--plugin-dir requires an argument")
			}
			p.extraPluginDirs = append(p.extraPluginDirs, args[i+1])
			i++
		case strings.HasPrefix(args[i], "--plugin-dir="):
			p.extraPluginDirs = append(p.extraPluginDirs, strings.TrimPrefix(args[i], "--plugin-dir="))
		default:
			return p, fmt.Errorf("unknown flag %q (use -- to pass arguments to the provider)", args[i])
		}
	}
	// Apply the build-time default profile only when the caller did
	// not pin a profile (flag/env) or pin an explicit provider — an
	// explicit --provider opts out of the profile-driven flow.
	if p.profile == "" && !p.providerExplicit && buildcfg.DefaultProfile != "" {
		p.profile = buildcfg.DefaultProfile
	}
	return p, nil
}

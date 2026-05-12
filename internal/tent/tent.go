// Package tent builds the podman invocation that wraps a provider
// binary. It is consumed by cmd/clown's tentExecutor; everything in
// here is pure (no IO) so the argv shape is testable in isolation.
//
// FDR-0007 (docs/features/0007-tent.md) is the design record. This
// package implements the tracer-bullet shape: opt-in, claude-only,
// --network=host, /nix/store bind-mounted read-only, working tree
// and ~/.claude bind-mounted writable.
package tent

import (
	"fmt"
	"os"
	"path/filepath"
)

// Options describes the container shape tent should construct. Zero
// values are not meaningful — callers should populate at least Image,
// Workdir, and Home (OptionsFromEnv does this).
type Options struct {
	// Image is the podman image reference (e.g. "clown-tent:1.2.3").
	Image string

	// Workdir is the host path that becomes the container's working
	// directory. Bind-mounted at the same path inside the container.
	Workdir string

	// Home is the user's home directory on the host. ~/.claude and
	// ~/.config/claude are bind-mounted writable so provider creds
	// and config persist across runs.
	Home string

	// TmpDir is the host temp directory (typically /tmp). Bind-
	// mounted writable so the plugin-host pipeline's staged plugin
	// dirs (created via os.MkdirTemp) are reachable from inside the
	// container at the same path. Pass an empty string to skip.
	TmpDir string

	// PluginDirs lists host paths to bind-mount read-only. The raw
	// (user-facing) plugin directories — anything under /nix/store
	// is already covered by the read-only /nix/store mount, so this
	// is mainly for --plugin-dir paths supplied at the command line.
	PluginDirs []string

	// EnvPassthrough lists environment variable names whose values
	// should be forwarded into the container. Anything not in this
	// list is intentionally invisible to the wrapped provider.
	EnvPassthrough []string

	// Tty signals that podman should allocate a pseudo-TTY (-t).
	// Claude's TUI requires this when stdin and stdout are real
	// terminals; in non-interactive contexts (--print mode, CI
	// pipelines) -t mangles output and must stay off.
	Tty bool

	// PathOverride, when non-empty, sets the container's PATH
	// explicitly via --env PATH=<value>. Use this when the caller
	// wants a curated PATH (e.g. the host devshell's PATH filtered
	// to /nix/store entries via FilterPathToNixStore) rather than
	// the image's default. The tent image bakes no baseline PATH,
	// so this is a clean replacement rather than a prepend.
	PathOverride string
}

// DefaultEnvPassthrough is the env-var allowlist for the tracer
// bullet. Narrow on purpose: HOME/USER/TERM for a usable shell,
// ANTHROPIC_API_KEY for claude auth, NO_PROXY/HTTP(S)_PROXY for
// users behind a corporate proxy.
//
// XDG vars are forwarded so claude finds its config under
// $XDG_CONFIG_HOME/claude when set. If a user points XDG_CONFIG_HOME
// at a non-default path, the corresponding directory must also be
// bind-mounted into the container — the tracer bullet only mounts
// ~/.claude and ~/.config/claude. Custom XDG path mounting is a
// follow-up (tracked alongside per-provider profiles in FDR-0007).
//
// LANG and LC_ALL are deliberately NOT in this list. The tent image
// pins both to C.UTF-8 in its Env config (see flake.nix) because
// glibc 2.35+ has C.UTF-8 built in but no archive for the host's
// typical en_US.UTF-8 is reachable inside the namespace, which would
// otherwise produce a `setlocale: cannot change locale` warning on
// every subshell. claude's TUI only needs UTF-8 char handling, not
// en_US collation/date formatting.
var DefaultEnvPassthrough = []string{
	"HOME",
	"USER",
	"TERM",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"XDG_CACHE_HOME",
}

// OptionsFromEnv builds Options from the process environment.
// `image` is the buildcfg-baked image reference; `pluginDirs` is the
// post-compilation plugin dir list from the plugin-host pipeline.
func OptionsFromEnv(image string, pluginDirs []string) (Options, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Options{}, fmt.Errorf("tent: getwd: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, fmt.Errorf("tent: userhomedir: %w", err)
	}
	return Options{
		Image:          image,
		Workdir:        cwd,
		Home:           home,
		TmpDir:         os.TempDir(),
		PluginDirs:     pluginDirs,
		EnvPassthrough: DefaultEnvPassthrough,
		Tty:            stdioIsTerminal(),
	}, nil
}

// stdioIsTerminal reports whether both stdin and stdout are TTYs.
// We require both: -t with a non-terminal stdout mangles output (the
// PTY layer adds CRLF / ANSI escapes that downstream consumers
// don't expect), and -t with non-terminal stdin gives podman no
// place to wire the master end of the pty.
func stdioIsTerminal() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

func isCharDevice(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// BuildArgs assembles the full podman argv that runs `claudeBinary
// claudeArgs...` inside the tent container. The first element is the
// podman subcommand ("run"); callers prepend the podman binary path
// as argv[0] separately.
//
// Shape (mounts elided for brevity):
//
//	run --rm -i --network=host --userns=keep-id
//	    --volume /nix/store:/nix/store:ro
//	    --volume <workdir>:<workdir>
//	    --volume <home>/.claude:<home>/.claude
//	    --volume <home>/.config/claude:<home>/.config/claude
//	    --volume <plugin-dir>:<plugin-dir>:ro       (per dir)
//	    --workdir <workdir>
//	    --env <NAME>                                 (per allowlisted var)
//	    <image>
//	    <claudeBinary> <claudeArgs...>
func BuildArgs(claudeBinary string, claudeArgs []string, opts Options) []string {
	args := []string{
		"run",
		"--rm",
		"-i",
	}
	if opts.Tty {
		args = append(args, "-t")
	}
	args = append(args,
		"--network=host",
		"--userns=keep-id",
		"--volume", "/nix/store:/nix/store:ro",
	)

	if opts.Workdir != "" {
		args = append(args, "--volume", fmt.Sprintf("%s:%s", opts.Workdir, opts.Workdir))
	}
	if opts.Home != "" {
		claudeDir := filepath.Join(opts.Home, ".claude")
		configDir := filepath.Join(opts.Home, ".config", "claude")
		claudeJSON := filepath.Join(opts.Home, ".claude.json")
		args = append(args,
			"--volume", fmt.Sprintf("%s:%s", claudeDir, claudeDir),
			"--volume", fmt.Sprintf("%s:%s", configDir, configDir),
			"--volume", fmt.Sprintf("%s:%s", claudeJSON, claudeJSON),
		)
	}
	if opts.TmpDir != "" {
		args = append(args, "--volume", fmt.Sprintf("%s:%s", opts.TmpDir, opts.TmpDir))
	}
	for _, dir := range opts.PluginDirs {
		if dir == "" {
			continue
		}
		args = append(args, "--volume", fmt.Sprintf("%s:%s:ro", dir, dir))
	}

	if opts.Workdir != "" {
		args = append(args, "--workdir", opts.Workdir)
	}
	for _, name := range opts.EnvPassthrough {
		if name == "" {
			continue
		}
		args = append(args, "--env", name)
	}
	if opts.PathOverride != "" {
		args = append(args, "--env", "PATH="+opts.PathOverride)
	}

	args = append(args, opts.Image, claudeBinary)
	args = append(args, claudeArgs...)
	return args
}

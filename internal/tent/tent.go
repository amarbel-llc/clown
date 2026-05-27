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
	"runtime"
)

// SSHAuthSockInVM is the well-known in-VM path where podman-machine's
// SSH-agent forwarder publishes the host's $SSH_AUTH_SOCK. Mirrors
// the Docker Desktop / OrbStack / Lima convention so containers know
// where to look on macOS. The forwarder is the
// `dev-tent-ssh-forward` flake app (see flake.nix). When the forwarder
// is not running, bind-mounting this path will fail at podman-run
// time with "not a socket" — recoverable by starting the forwarder.
//
// Why we don't bind the host $SSH_AUTH_SOCK directly on darwin:
// podman-machine's virtiofs/9p layer cannot proxy AF_UNIX sockets,
// so the host socket inode is visible inside the VM but `statfs`
// returns "operation not supported" when podman tries to bind-mount
// it into a container. See containers/podman#23245 / #23785.
const SSHAuthSockInVM = "/run/host-services/ssh-auth.sock"

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

	// ExtraBinds lists additional host paths to bind-mount writable
	// at the same path inside the container. Used to expose host-side
	// metadata that lives outside the Workdir bind — most notably the
	// parent repo's `.git/` directory when Workdir is inside a git
	// worktree (see DiscoverGitWorktreeBinds). Writable so in-container
	// git operations (commit, switch) work.
	ExtraBinds []string

	// ReadOnlyBinds lists additional host paths to bind-mount
	// read-only at the same path inside the container. Source of the
	// FDR-0007 C+F allowlist (2026-05-19 update): /nix/var (daemon
	// socket + profile-link targets), /etc/nix, ~/.nix-profile,
	// ~/.gitconfig, ~/.config/{git,nix}. DefaultReadOnlyBinds populates
	// this with the subset of candidates that actually exist on the
	// host.
	ReadOnlyBinds []string

	// SSHAuthSock is the host path to the SSH agent socket
	// (typically $SSH_AUTH_SOCK). When non-empty, the socket is
	// bind-mounted at the same path inside the container and
	// SSH_AUTH_SOCK is forwarded via env. This lets the in-tent
	// agent use the host's ssh-agent (or pivy-agent, which also
	// signs git commits via the same socket) for git push + commit
	// signing without any key material entering the tent.
	SSHAuthSock string

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

	// ConnectionName, when non-empty, becomes `--connection <name>`
	// in podman's argv *before* the subcommand. Selects a
	// non-default podman connection (= machine name on darwin).
	// Burned in from buildcfg.PodmanMachineName by mkClownGo so
	// downstream consumers like packages.dev can target an
	// isolated dev-loop machine without touching the user's
	// eng-managed podman-machine-default.
	ConnectionName string
}

// PodmanConnectionArgs returns the argv prefix that selects a
// non-default podman connection: ["--connection", name] when name
// is non-empty, nil otherwise. The flag must come *before* the
// subcommand (podman --connection <name> <subcommand> ...).
// Centralized here so cmd/clown's three podman call sites
// (`image exists`, `load`, `run`) all use the same shape.
func PodmanConnectionArgs(name string) []string {
	if name == "" {
		return nil
	}
	return []string{"--connection", name}
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
	// SSH_AUTH_SOCK is paired with the SSHAuthSock bind-mount field:
	// the env var tells in-tent tools where to find the socket, the
	// bind-mount makes that path resolve. Forwarding the env var
	// without the mount would leak a dangling path; mounting without
	// the env var would leave tools looking at the wrong place. The
	// pair is the unit.
	"SSH_AUTH_SOCK",
	// SSH_HOME is a home-manager-driven convention (used in
	// amarbel-llc/eng): the user's ssh wrapper defaults --user-known-hosts
	// and -F to $SSH_HOME/{known_hosts,config}, with SSH_HOME pointing
	// at ~/.config/ssh by convention. Hosts that don't follow this
	// convention have SSH_HOME unset and the var passthrough is a
	// no-op; hosts that do need it to be set, and need ~/.config/ssh
	// bind-mounted (handled by DefaultReadOnlyBindCandidates).
	"SSH_HOME",
}

// OptionsFromEnv builds Options from the process environment.
// `image` is the buildcfg-baked image reference; `connection` is the
// buildcfg-baked podman connection name (empty = use podman's
// default connection); `pluginDirs` is the post-compilation plugin
// dir list from the plugin-host pipeline.
func OptionsFromEnv(image, connection string, pluginDirs []string) (Options, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Options{}, fmt.Errorf("tent: getwd: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, fmt.Errorf("tent: userhomedir: %w", err)
	}
	extras, err := DiscoverGitWorktreeBinds(cwd)
	if err != nil {
		return Options{}, fmt.Errorf("tent: discover git binds: %w", err)
	}
	return Options{
		Image:          image,
		Workdir:        cwd,
		Home:           home,
		TmpDir:         os.TempDir(),
		PluginDirs:     pluginDirs,
		ExtraBinds:     extras,
		ReadOnlyBinds:  DefaultReadOnlyBinds(home),
		SSHAuthSock:    sshAuthSockForRuntime(),
		EnvPassthrough: DefaultEnvPassthrough,
		Tty:            stdioIsTerminal(),
		ConnectionName: connection,
	}, nil
}

// sshAuthSockForRuntime picks the path that should be bind-mounted as
// the in-tent $SSH_AUTH_SOCK. On linux native, $SSH_AUTH_SOCK from
// the host environment is bound directly (the host and container
// share an fs namespace through the bind). On darwin, where
// podman-machine's virtiofs/9p layer cannot proxy AF_UNIX sockets,
// we instead use the well-known in-VM path published by the
// `dev-tent-ssh-forward` flake app (see SSHAuthSockInVM). The
// caller (dev-tent-machine-up, or a separate `nix run
// .#dev-tent-ssh-forward` invocation) is responsible for spawning
// the forwarder; clown returns an empty path if the host env var
// isn't set on darwin so a missing forwarder degrades cleanly to
// "no agent" rather than "broken bind mount".
func sshAuthSockForRuntime() string {
	host := os.Getenv("SSH_AUTH_SOCK")
	if host == "" {
		return ""
	}
	if runtime.GOOS == "darwin" {
		// Containers see the forwarded socket at SSHAuthSockInVM.
		// The bind source IS the in-VM path because podman run's
		// --volume source is interpreted inside the VM, not on the
		// host.
		return SSHAuthSockInVM
	}
	return host
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
//	run --rm -i --network=host
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
	args := append([]string{}, PodmanConnectionArgs(opts.ConnectionName)...)
	args = append(args,
		"run",
		"--rm",
		"-i",
	)
	if opts.Tty {
		args = append(args, "-t")
	}
	args = append(args,
		"--network=host",
		// `--userns=keep-id` was here originally for linux native
		// rootless podman: it maps the host user to root inside the
		// container's user namespace so bind-mounted files retain
		// the host user's ownership. On darwin under applehv,
		// however, it causes `claude -p ...` to hang indefinitely
		// (verified 2026-05-27; isolated by bisecting against a
		// minimal-repro `podman run`). The bind-mount source files
		// are already in the VM filesystem with VM-side UIDs, so
		// keep-id is unnecessary on darwin. Removing entirely for
		// now; if linux native bind-mount ownership issues resurface
		// we should re-introduce it gated on `runtime.GOOS == "linux"`.
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
	for _, dir := range opts.ExtraBinds {
		if dir == "" {
			continue
		}
		args = append(args, "--volume", fmt.Sprintf("%s:%s", dir, dir))
	}
	for _, dir := range opts.ReadOnlyBinds {
		if dir == "" {
			continue
		}
		args = append(args, "--volume", fmt.Sprintf("%s:%s:ro", dir, dir))
	}
	if opts.SSHAuthSock != "" {
		// Socket is bind-mounted writable (rw) because some SSH agents
		// expect bidirectional ioctls on the socket; :ro on a unix
		// socket is ineffective for socket-level ops anyway. The host
		// agent decides whether to honor any requests — the mount
		// itself doesn't grant capability beyond what the agent already
		// exposes.
		args = append(args, "--volume", fmt.Sprintf("%s:%s", opts.SSHAuthSock, opts.SSHAuthSock))
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

package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"code.linenisgreat.com/clown/internal/agentscope"
)

// runInAgentScope launches argv (the provider binary first) in the agent
// scope described by the spec at specPath (clown#244, FDR 0020). clown stays
// in the frontend scope, relays the terminal, and writes the spec's result
// file once the unit is active. Any failure refuses the launch: there is no
// unconfined fallback.
func runInAgentScope(specPath, stagingRoot string, argv, extraEnv []string, logger *slog.Logger) int {
	launch, err := prepareAgentScope(specPath, argv, extraEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: agent scope: %v\n", err)
		return 1
	}
	if stagingRoot != "" && !strings.HasPrefix(stagingRoot, launch.Spec.Worktree+"/") {
		launch.PlumbingRO = append(launch.PlumbingRO, stagingRoot)
	}
	stopProxy, err := startEgressProxy(&launch, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: agent scope: egress proxy: %v\n", err)
		return 1
	}
	defer stopProxy()
	code, err := agentscope.Run(launch, agentScopeTools(), logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: agent scope: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	resetTerminal()
	return code
}

// agentScopeTools resolves systemd-run and systemctl from PATH (overridable
// by CLOWN_SYSTEMD_RUN / CLOWN_SYSTEMCTL), deliberately not from a burned-in
// nixpkgs systemd: systemd-run serializes properties client-side, so it must
// be as new as the running user manager (PrivatePIDs= needs systemd >= 257).
func agentScopeTools() agentscope.Tools {
	return agentscope.Tools{
		SystemdRun: os.Getenv("CLOWN_SYSTEMD_RUN"),
		Systemctl:  os.Getenv("CLOWN_SYSTEMCTL"),
	}
}

// prepareAgentScope loads the spec and assembles the Launch: unit name, agent
// home, the in-unit exec shim, and clown's own plumbing binds (claude's config
// and transcripts, the job platform's state and runtime dirs, hook state).
func prepareAgentScope(specPath string, argv, extraEnv []string) (agentscope.Launch, error) {
	spec, err := agentscope.LoadSpec(specPath)
	if err != nil {
		return agentscope.Launch{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return agentscope.Launch{}, err
	}
	tools := agentScopeTools()
	managerKeys, err := tools.ManagerEnvKeys()
	if err != nil {
		return agentscope.Launch{}, err
	}
	agentHome := filepath.Join(spec.Worktree, ".tmp", "agent-home")
	plumbing, err := stageAgentHome(spec, agentHome, cwd)
	if err != nil {
		return agentscope.Launch{}, fmt.Errorf("staging the agent home: %w", err)
	}
	launch := agentscope.Launch{
		Spec:           spec,
		Unit:           agentscope.UnitName(spec.UnitNameHint),
		WorkDir:        cwd,
		AgentHome:      agentHome,
		FrontendEnv:    os.Environ(),
		ManagerEnvKeys: managerKeys,
		ExtraEnv:       append(extraEnv, "CLAUDE_CONFIG_DIR="+filepath.Join(agentHome, ".claude")),
		PlumbingRW:     plumbing.rw,
		PlumbingRO:     plumbing.ro,
		Shim:           []string{clownExePath(), "agent-exec"},
		Argv:           argv,
	}
	return launch, nil
}

// startEgressProxy runs the unit's frontend egress proxy on a per-session
// unix socket under $XDG_RUNTIME_DIR/clown/agent/<unit>/ and records it on
// the launch. The allowlist is the spec's egress_allow, else the default,
// plus the host of ANTHROPIC_BASE_URL when clown routes the model API
// elsewhere.
func startEgressProxy(launch *agentscope.Launch, logger *slog.Logger) (stop func(), err error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		return nil, errors.New("XDG_RUNTIME_DIR is unset; nowhere to put the proxy socket")
	}
	dir := filepath.Join(runtime, "clown", "agent", strings.TrimSuffix(launch.Unit, ".service"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	allow := agentscope.Allowlist(launch.Spec.EgressAllow)
	if len(allow) == 0 {
		allow = agentscope.DefaultEgressAllow
	}
	if hp := baseURLHostPort(os.Getenv("ANTHROPIC_BASE_URL")); hp != "" {
		allow = append(append(agentscope.Allowlist{}, allow...), hp)
	}
	proxy := &agentscope.EgressProxy{Allow: allow, Unit: launch.Unit, Logger: logger}
	sock := filepath.Join(dir, "egress.sock")
	if err := proxy.Listen(sock); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	launch.EgressSocket = sock
	return func() {
		_ = proxy.Close()
		_ = os.RemoveAll(dir)
	}, nil
}

// baseURLHostPort returns host:port of an http(s) URL, or "".
func baseURLHostPort(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "http" {
		return u.Host + ":80"
	}
	return u.Host + ":443"
}

type plumbingBinds struct{ rw, ro []string }

// stageAgentHome prepares the per-session HOME inside the worktree and
// returns the operator paths clown's own components need bound in.
//
// claude gets its own CLAUDE_CONFIG_DIR, seeded from the operator's config
// once, so an edit to settings or .claude.json from inside (a hook written
// there would run in unconfined sessions) never reaches the operator's copy.
// The operator's credentials file and this project's transcript dir are bound
// over the seeded copies so login and `clown resume` keep working.
func stageAgentHome(spec agentscope.Spec, agentHome, cwd string) (plumbingBinds, error) {
	var p plumbingBinds
	opHome := spec.OperatorHome
	cfg := filepath.Join(agentHome, ".claude")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		return p, err
	}
	seeds := map[string]string{
		filepath.Join(opHome, ".claude.json"):             filepath.Join(cfg, ".claude.json"),
		filepath.Join(opHome, ".claude", "settings.json"): filepath.Join(cfg, "settings.json"),
	}
	for src, dst := range seeds {
		if err := copyIfMissing(src, dst); err != nil {
			return p, err
		}
	}

	creds := filepath.Join(opHome, ".claude", ".credentials.json")
	if _, err := os.Stat(creds); err == nil {
		dst := filepath.Join(cfg, ".credentials.json")
		if err := touch(dst); err != nil {
			return p, err
		}
		p.rw = append(p.rw, creds+":"+dst)
	}
	slug := claudeProjectSlug(cwd)
	transcripts := filepath.Join(opHome, ".claude", "projects", slug)
	if err := os.MkdirAll(transcripts, 0o700); err != nil {
		return p, err
	}
	inUnit := filepath.Join(cfg, "projects", slug)
	if err := os.MkdirAll(inUnit, 0o700); err != nil {
		return p, err
	}
	p.rw = append(p.rw, transcripts+":"+inUnit)

	// The same resolution the unit's env uses (agentscope.OperatorXDG), so a
	// tool inside writes exactly where these binds land. Created up front: a
	// missing bind source is skipped, and the hidden home is read-only inside.
	environ := os.Environ()
	state := agentscope.OperatorXDG(environ, opHome, "XDG_STATE_HOME")
	logs := agentscope.OperatorXDG(environ, opHome, "XDG_LOG_HOME")
	dirs := []string{
		filepath.Join(state, "ringmaster"),
		filepath.Join(state, "troupe"),
		filepath.Join(state, "clown", "hook-toolcost"),
		filepath.Join(state, "clown", "hook-tee"),
		filepath.Join(logs, "moxy", "hooks"),
	}
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		dirs = append(dirs, filepath.Join(runtime, "ringmaster"), filepath.Join(runtime, "troupe"))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return p, err
		}
	}
	p.rw = append(p.rw, dirs...)
	if pw := os.Getenv("TROUPE_XMPP_PASSWORD_FILE"); filepath.IsAbs(pw) {
		p.ro = append(p.ro, pw)
	}
	// The user's nix profile links (home-manager's PATH entries) live under
	// the hidden home; they only point into /nix/store.
	p.ro = append(p.ro,
		filepath.Join(opHome, ".nix-profile"),
		filepath.Join(state, "nix", "profiles"),
	)
	return p, nil
}

// claudeProjectSlug is claude's per-project directory name under
// ~/.claude/projects: every non-alphanumeric byte of the cwd becomes '-'.
func claudeProjectSlug(cwd string) string {
	b := []byte(cwd)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

func copyIfMissing(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func touch(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// runAgentExec is the in-unit exec shim
// (`clown agent-exec [--egress-relay ADDR=SOCK] -- argv...`). It first starts
// the egress relay (the unit's only way out) and waits until it listens. When
// fd 0 is a terminal (the pty clown allocated in the frontend), it then starts
// a new session and takes that terminal as its controlling tty, so SIGWINCH
// and ^C reach the provider. Finally it execs argv.
func runAgentExec(args []string) int {
	var relay string
	for len(args) > 0 && args[0] != "--" {
		if args[0] == "--egress-relay" && len(args) > 1 {
			relay, args = args[1], args[2:]
			continue
		}
		fmt.Fprintf(os.Stderr, "clown agent-exec: unknown argument %q\n", args[0])
		return 2
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown agent-exec: usage: clown agent-exec [--egress-relay ADDR=SOCK] -- <argv>")
		return 2
	}
	if relay != "" {
		if err := startEgressRelay(relay); err != nil {
			fmt.Fprintf(os.Stderr, "clown agent-exec: egress relay: %v\n", err)
			return 126
		}
	}
	if term.IsTerminal(0) {
		if _, err := unix.Setsid(); err != nil && !errors.Is(err, unix.EPERM) {
			fmt.Fprintf(os.Stderr, "clown agent-exec: setsid: %v\n", err)
			return 126
		}
		if err := unix.IoctlSetInt(0, unix.TIOCSCTTY, 0); err != nil {
			fmt.Fprintf(os.Stderr, "clown agent-exec: acquiring the controlling tty: %v\n", err)
			return 126
		}
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown agent-exec: %v\n", err)
		return 127
	}
	err = syscall.Exec(path, args, os.Environ())
	fmt.Fprintf(os.Stderr, "clown agent-exec: exec %s: %v\n", path, err)
	return 126
}

// startEgressRelay starts `clown egress-relay ADDR SOCK` as a child that
// outlives the exec (it stays in the unit's cgroup and dies with it) and waits
// for it to report, on an inherited pipe, that it is listening.
func startEgressRelay(spec string) error {
	addr, sock, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("--egress-relay %q is not ADDR=SOCK", spec)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	cmd := exec.Command(clownExePath(), "egress-relay", addr, sock)
	cmd.ExtraFiles = []*os.File{w}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		w.Close()
		return err
	}
	w.Close()
	buf := make([]byte, 1)
	if n, _ := r.Read(buf); n != 1 {
		return errors.New("relay exited before listening")
	}
	return nil
}

// runEgressRelay is `clown egress-relay ADDR SOCK`: the in-unit end of the
// egress path. fd 3, when present, gets one byte once the listener is up.
func runEgressRelay(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "clown egress-relay: usage: clown egress-relay ADDR SOCK")
		return 2
	}
	ready := os.NewFile(3, "ready")
	err := agentscope.RunRelay(args[0], args[1], func() {
		if ready != nil {
			_, _ = ready.Write([]byte{1})
			_ = ready.Close()
		}
	})
	fmt.Fprintf(os.Stderr, "clown egress-relay: %v\n", err)
	return 1
}

// runEgressConnect is `clown egress-connect SOCK HOST PORT`, an ssh
// ProxyCommand that tunnels stdin/stdout through the egress proxy.
func runEgressConnect(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "clown egress-connect: usage: clown egress-connect SOCK HOST PORT")
		return 2
	}
	if err := agentscope.Connect(args[0], net.JoinHostPort(args[1], args[2]), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "clown egress-connect: %v\n", err)
		return 1
	}
	return 0
}

// runAgentScopeRun runs an arbitrary command in the agent scope through the
// same launcher claude uses: `clown agent-scope-run --spec <path> -- argv...`.
// It exists for the host confinement test (zz-tests_bats/agent_scope.bats)
// and for probing; it takes no provider, plugin host or prompt.
func runAgentScopeRun(args []string) int {
	var specPath string
	for len(args) > 0 && args[0] != "--" {
		switch {
		case args[0] == "--spec" && len(args) > 1:
			specPath, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--spec="):
			specPath, args = strings.TrimPrefix(args[0], "--spec="), args[1:]
		default:
			fmt.Fprintf(os.Stderr, "clown agent-scope-run: unknown argument %q\n", args[0])
			return 2
		}
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if specPath == "" || len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown agent-scope-run: usage: clown agent-scope-run --spec <path> -- <argv>")
		return 2
	}
	return runInAgentScope(specPath, "", args, nil, nil)
}

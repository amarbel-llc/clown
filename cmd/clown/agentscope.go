package main

import (
	"encoding/json"
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
//
// forwardAddrs are the loopback addresses of this session's frontend-class
// plugin servers; each is forwarded into the unit (and nothing else on the
// host's loopback is reachable from it).
func runInAgentScope(specPath, stagingRoot string, forwardAddrs, argv, extraEnv []string, logger *slog.Logger) int {
	launch, err := prepareAgentScope(specPath, argv, extraEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: agent scope: %v\n", err)
		return 1
	}
	if stagingRoot != "" && !strings.HasPrefix(stagingRoot, launch.Spec.Worktree+"/") {
		launch.PlumbingRO = append(launch.PlumbingRO, stagingRoot)
	}
	stopEndpoints, err := startFrontendEndpoints(&launch, append(forwardAddrs, xmppForward(&launch)...), logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: agent scope: frontend endpoints: %v\n", err)
		return 1
	}
	defer stopEndpoints()
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

// startFrontendEndpoints runs, in the frontend, every unix socket the unit
// reaches the outside through, under $XDG_RUNTIME_DIR/clown/agent/<unit>/:
// the egress proxy and one PortForward per forwarded loopback address. All
// accept only callers in the unit's cgroup. They are recorded on the launch
// (EgressSocket, Forwards) so the unit binds them and the shim relays them.
//
// The proxy's allowlist is the spec's egress_allow, else the default, plus
// the host of ANTHROPIC_BASE_URL when clown routes the model API elsewhere.
func startFrontendEndpoints(launch *agentscope.Launch, forwardAddrs []string, logger *slog.Logger) (stop func(), err error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		return nil, errors.New("XDG_RUNTIME_DIR is unset; nowhere to put the unit's sockets")
	}
	dir := filepath.Join(runtime, "clown", "agent", strings.TrimSuffix(launch.Unit, ".service"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var closers []func() error
	stop = func() {
		for _, c := range closers {
			_ = c()
		}
		_ = os.RemoveAll(dir)
	}
	socket := func(name string) (string, error) {
		p := filepath.Join(dir, name)
		if len(p) >= 108 {
			return "", fmt.Errorf("socket path %q exceeds the unix socket limit; shorten unit_name_hint", p)
		}
		return p, nil
	}

	allow := agentscope.Allowlist(launch.Spec.EgressAllow)
	if len(allow) == 0 {
		allow = agentscope.DefaultEgressAllow
	}
	if hp := baseURLHostPort(os.Getenv("ANTHROPIC_BASE_URL")); hp != "" {
		allow = append(append(agentscope.Allowlist{}, allow...), hp)
	}
	proxy := &agentscope.EgressProxy{Allow: allow, Unit: launch.Unit, Logger: logger}
	sock, err := socket("egress.sock")
	if err == nil {
		err = proxy.Listen(sock)
	}
	if err != nil {
		stop()
		return nil, err
	}
	closers = append(closers, proxy.Close)
	launch.EgressSocket = sock

	seen := map[string]bool{}
	for _, addr := range forwardAddrs {
		_, port, perr := net.SplitHostPort(addr)
		if perr != nil || seen[addr] {
			continue
		}
		seen[addr] = true
		fwd := &agentscope.PortForward{Target: addr, Unit: launch.Unit, Logger: logger}
		sock, err := socket("fwd-" + port + ".sock")
		if err == nil {
			err = fwd.Listen(sock)
		}
		if err != nil {
			stop()
			return nil, err
		}
		closers = append(closers, fwd.Close)
		launch.Forwards = append(launch.Forwards, agentscope.Relay{Listen: "127.0.0.1:" + port, Socket: sock})
	}
	return stop, nil
}

// xmppForward returns the XMPP server address to forward when this session
// has its own xmpp-native credential (troupe dials TROUPE_XMPP_HOST:PORT,
// default 127.0.0.1:5222, with TLS ServerName = the domain, so no DNS is
// needed). Inside the unit troupe then dials the relay on loopback; a
// non-loopback host is pointed there with TROUPE_XMPP_HOST.
func xmppForward(launch *agentscope.Launch) []string {
	if os.Getenv("TROUPE_TRANSPORT") != "xmpp-native" || os.Getenv("TROUPE_XMPP_PASSWORD_FILE") == "" {
		return nil
	}
	host := firstNonEmpty(os.Getenv("TROUPE_XMPP_HOST"), "127.0.0.1")
	port := firstNonEmpty(os.Getenv("TROUPE_XMPP_PORT"), "5222")
	if host != "127.0.0.1" {
		launch.ExtraEnv = append(launch.ExtraEnv, "TROUPE_XMPP_HOST=127.0.0.1")
	}
	return []string{net.JoinHostPort(host, port)}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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
	// Only this session's own XMPP credential, and only when it names its
	// principal: a mismatch means a credential inherited from another
	// session (clown#267), which must never enter a unit.
	if pw := os.Getenv("TROUPE_XMPP_PASSWORD_FILE"); pw != "" {
		user := os.Getenv("TROUPE_XMPP_USER")
		if !filepath.IsAbs(pw) || !xmppCredentialConsistent(user, pw) {
			return p, fmt.Errorf("TROUPE_XMPP_PASSWORD_FILE %q does not name TROUPE_XMPP_USER %q's principal; refusing to bind another session's credential", pw, user)
		}
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
// (`clown agent-exec [--relay ADDR=SOCK]... -- argv...`). It first starts the
// relays (the unit's only ways out: the egress proxy and the forwarded
// loopback services) and waits until they listen. When fd 0 is a terminal
// (the pty clown allocated in the frontend), it then starts a new session and
// takes that terminal as its controlling tty, so SIGWINCH and ^C reach the
// provider. Finally it execs argv.
func runAgentExec(args []string) int {
	var relays []string
	for len(args) > 0 && args[0] != "--" {
		if args[0] == "--relay" && len(args) > 1 {
			relays, args = append(relays, args[1]), args[2:]
			continue
		}
		fmt.Fprintf(os.Stderr, "clown agent-exec: unknown argument %q\n", args[0])
		return 2
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown agent-exec: usage: clown agent-exec [--relay ADDR=SOCK]... -- <argv>")
		return 2
	}
	if len(relays) > 0 {
		if err := startRelays(relays); err != nil {
			fmt.Fprintf(os.Stderr, "clown agent-exec: relays: %v\n", err)
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

// startRelays starts `clown agent-relay ADDR=SOCK...` as a child that outlives
// the exec (it stays in the unit's cgroup and dies with it) and waits for it
// to report, on an inherited pipe, that every listener is up.
func startRelays(relays []string) error {
	for _, r := range relays {
		if _, err := agentscope.ParseRelay(r); err != nil {
			return err
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	cmd := exec.Command(clownExePath(), append([]string{"agent-relay"}, relays...)...)
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

// runAgentRelay is `clown agent-relay ADDR=SOCK...`: the in-unit end of the
// unit's ways out. fd 3, when present, gets one byte once every listener is
// up.
func runAgentRelay(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "clown agent-relay: usage: clown agent-relay ADDR=SOCK...")
		return 2
	}
	relays := make([]agentscope.Relay, 0, len(args))
	for _, a := range args {
		r, err := agentscope.ParseRelay(a)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clown agent-relay: %v\n", err)
			return 2
		}
		relays = append(relays, r)
	}
	ready := os.NewFile(3, "ready")
	err := agentscope.RunRelays(relays, func() {
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
	})
	fmt.Fprintf(os.Stderr, "clown agent-relay: %v\n", err)
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

// runAgentScopeCapabilities is `clown agent-scope-capabilities`: prints
// {"version":1,"capabilities":[...]} so spinclass can refuse a confined launch
// before it starts, from the frontend, with the same clown the spawn runs.
func runAgentScopeCapabilities(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "clown agent-scope-capabilities: takes no arguments")
		return 2
	}
	out, err := json.Marshal(struct {
		Version      int      `json:"version"`
		Capabilities []string `json:"capabilities"`
	}{agentscope.SpecVersion, agentscope.Capabilities})
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown agent-scope-capabilities: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}

// runAgentScopeRun runs an arbitrary command in the agent scope through the
// same launcher claude uses:
// `clown agent-scope-run --spec <path> [--forward HOST:PORT]... -- argv...`.
// --forward stands in for a frontend-class plugin server's address. It exists
// for the host confinement test (zz-tests_bats/agent_scope.bats) and for
// probing; it takes no provider, plugin host or prompt.
func runAgentScopeRun(args []string) int {
	var specPath string
	var forwards []string
	for len(args) > 0 && args[0] != "--" {
		switch {
		case args[0] == "--spec" && len(args) > 1:
			specPath, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--spec="):
			specPath, args = strings.TrimPrefix(args[0], "--spec="), args[1:]
		case args[0] == "--forward" && len(args) > 1:
			forwards, args = append(forwards, args[1]), args[2:]
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
	return runInAgentScope(specPath, "", forwards, args, nil, nil)
}

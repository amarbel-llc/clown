package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The restart itself (spinclass#419). On a "restart" verdict the idle runner
// records a relaunchPlan and stops the provider; runWithFlags then unwinds as
// on any exit (the plugin host stops the serve bridge, the agent unit is
// stopped, the idle and control sockets close), and main, instead of exiting,
// waits for the old serve socket to go and re-execs `clown` from PATH with
// --resume <principal>. The same process, so the same terminal and the same
// multiplexer pane.
//
// A restart is not a close: nothing on this path revokes or releases the
// session's identity. The frontend never runs `troupe mint-revoke` on any exit
// (only juggler's own sessions do); the relaunched clown re-runs the
// idempotent `troupe mint --session-key <principal>` and re-registers its
// presence under the same key.

// relaunchPlan is what main needs to relaunch this frontend once run returns.
type relaunchPlan struct {
	principal string
	reason    string
	// argv is clown's arguments, without argv[0].
	argv []string
	// worktree is the session's working directory at launch; "" skips the
	// check.
	worktree string
}

// killProcess SIGKILLs p through its handle: once the frontend has reaped it
// this returns os.ErrProcessDone instead of signalling a pid that may since
// belong to another process.
func killProcess(p *os.Process) error { return p.Signal(syscall.SIGKILL) }

var (
	// pendingRelaunch is set by the idle runner just before it stops the
	// provider; main relaunches when it is set.
	pendingRelaunch atomic.Pointer[relaunchPlan]
	// providerRunning is true while runProvider waits on the provider, the
	// only time stopping it unwinds to main.
	providerRunning atomic.Bool
	// providerKill SIGKILLs the running provider; set and cleared with
	// providerRunning, under providerMu.
	providerKill func() error
	providerMu   sync.Mutex
)

// restartUnderway reports that a restart has recorded its plan: from then
// until the relaunch, the idle poke runs nothing and the control socket
// refuses another restart.
func restartUnderway() bool { return pendingRelaunch.Load() != nil }

// setProviderRunning marks the provider running (its SIGTERM forwarding is
// in place) with how to kill it.
func setProviderRunning(kill func() error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	providerKill = kill
	providerRunning.Store(true)
}

func clearProviderRunning() {
	providerMu.Lock()
	defer providerMu.Unlock()
	providerRunning.Store(false)
	providerKill = nil
}

// killProvider SIGKILLs the running provider, only while it is still recorded
// as running. The check takes the lock clearProviderRunning takes; the kill
// runs after it is released, so a hung kill (systemctl) cannot hold up the
// provider's exit. That leaves no reused-pid window: the direct and pty kills
// go through the process handle (os.ErrProcessDone once reaped) and the unit
// kill names this launch's own unit.
func killProvider() error {
	providerMu.Lock()
	kill, running := providerKill, providerRunning.Load()
	providerMu.Unlock()
	if !running || kill == nil {
		return nil
	}
	return kill()
}

// storePlanIfRunning records plan only while the provider still runs, in
// the same critical section as clearProviderRunning, so a provider that has
// just exited never gets a plan that main would act on.
func storePlanIfRunning(plan *relaunchPlan) bool {
	providerMu.Lock()
	defer providerMu.Unlock()
	if !providerRunning.Load() {
		return false
	}
	pendingRelaunch.Store(plan)
	return true
}

// restartArgv is this launch's argv for the relaunch: the resolved flags
// (reexecArgv, as the [attach] re-exec uses), with any --session-id/--resume
// replaced by --resume <principal>, and the [attach] id kept for the inner
// clown of a multiplexer so it does not wrap again.
func restartArgv(flags parsedFlags, attachedID string) []string {
	f := flags
	// The --resume goes before any `--`, after which claude reads operands.
	kept := withoutClaudeSessionArgs(flags.forwarded)
	end := len(kept)
	if i := slices.Index(kept, "--"); i >= 0 {
		end = i
	}
	f.forwarded = slices.Concat(kept[:end], []string{"--resume", flags.identity.Key}, kept[end:])
	var argv []string
	if attachedID != "" {
		// The inner of an [attach] wrap may not have the marker in its
		// environment (unattendedCarrierFlag), so its restart carries it too.
		argv = append(append(argv, attachIDFlag, attachedID), unattendedCarrierArgs()...)
	}
	return append(argv, f.reexecArgv()...)
}

// withoutClaudeSessionArgs drops claude's --session-id (with its value, in
// either form) and exactly the --resume/-r occurrences resumeOccurrences
// reads (with a separate value token). Everything else is kept, including
// operands after `--` and a short cluster carrying r (-cr), which the
// confinement gate refuses while any session is confined.
func withoutClaudeSessionArgs(args []string) []string {
	drop := map[int]bool{}
	for _, o := range resumeOccurrences(args) {
		if o.ambiguous {
			continue
		}
		drop[o.index] = true
		if o.takesNext {
			drop[o.index+1] = true
		}
	}
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i:]...)
		case drop[i]:
		case a == "--session-id":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(a, "--session-id="):
		default:
			out = append(out, a)
		}
	}
	return out
}

// launchEnviron is clown's environment as it started, before runWithFlags
// exported anything of its own (the minted XMPP credential, CLOWN_NAME, the
// group id): the relaunch derives those again.
var launchEnviron = os.Environ()

// restartEnvDropped are the launch-environment keys the relaunch (and the
// peek before it) does not inherit: any other session id, the launcher's
// one-shot profile approval, and a stale restart notice.
var restartEnvDropped = []string{"CLOWN_SESSION_ID", "CLAUDE_SESSION_ID", "CLOWN_PROFILE", "CLOWN_PROFILE_DIGEST", "CLOWN_PROFILE_SESSION", restartedReasonEnv}

// restartEnv is the relaunch's environment: the launch environment with the
// principal in CLOWN_SESSION_ID and the notice in CLOWN_RESTARTED_REASON. The
// launcher's one-shot profile approval is not replayed (the argv carries
// --profile, and the session is already pinned), and no other session id
// survives.
func restartEnv(environ []string, plan *relaunchPlan) []string {
	env := environWithout(environ, restartEnvDropped...)
	return append(env, "CLOWN_SESSION_ID="+plan.principal, restartedReasonEnv+"="+plan.reason)
}

// restartedReasonEnv carries the restart's reason to the relaunched frontend,
// which consumes it (consumeRestartedNotice) before starting anything.
const restartedReasonEnv = "CLOWN_RESTARTED_REASON"

// consumeRestartedNotice reads and unsets CLOWN_RESTARTED_REASON, returning
// the prompt notice for a relaunched session ("" for any other launch) and
// whether this launch is a restart's relaunch: the variable is set only by
// restartEnv, so its presence, not the notice text, is the marker.
func consumeRestartedNotice() (notice string, relaunched bool) {
	reason, ok := os.LookupEnv(restartedReasonEnv)
	if !ok {
		return "", false
	}
	_ = os.Unsetenv(restartedReasonEnv)
	if reason == "" {
		reason = "no reason given"
	}
	return restartedNotice(reason), true
}

// preMintPeekTimeout bounds the relaunch's own peek: it only narrows a window
// the old frontend's 20 s pre-exec peek already checked, so it must not hold
// up the launch long.
const preMintPeekTimeout = 5 * time.Second

// preMintPeek is the relaunched frontend's second look at its session
// (spinclass#419): before its first identity side effect (presence
// registration, then `troupe mint`), it asks `spinclass restart-preflight
// --peek` again, the same way the old frontend did before the exec
// (peekSessionState: launch env, principal, own process group), bounded at
// preMintPeekTimeout. A session closed since the old frontend's peek is then
// neither registered nor re-minted, so a close's revocation is not undone.
type preMintPeek struct {
	// spinclass is the binary; "" means not on PATH (unknown).
	spinclass string
	principal string
	environ   []string
	logPath   string
	// timeout, zero: preMintPeekTimeout.
	timeout time.Duration
}

// closed reports whether spinclass says the session is closed, in which case
// the relaunch must not register, mint or start anything. Anything else,
// including a failed, timed-out or unknown peek (a spinclass without
// --peek), lets it go on. It never revokes anything (clown never runs `troupe
// mint-revoke`).
func (p preMintPeek) closed() bool {
	if p.spinclass == "" {
		logSelfClose(p.logPath, "relaunched (%s): spinclass is not on PATH, session state unknown; starting", p.principal)
		return false
	}
	timeout := p.timeout
	if timeout == 0 {
		timeout = preMintPeekTimeout
	}
	state, err := peekSessionState(p.spinclass, p.principal, p.environ, timeout)
	switch {
	case err != nil:
		logSelfClose(p.logPath, "relaunched (%s): session state unknown before the mint (%v); starting", p.principal, err)
		return false
	case state == "closed":
		logSelfClose(p.logPath, "relaunched (%s): spinclass reports the session closed before the mint; not registering, minting or starting it (nothing revoked here)", p.principal)
		return true
	default:
		logSelfClose(p.logPath, "relaunched (%s): spinclass reports the session %s before the mint; starting", p.principal, state)
		return false
	}
}

// sessionIdentitySteps are a launch's first identity side effects, in their
// fixed order: for a restart's relaunch the peek, then the presence
// registration, then the XMPP mint. run is what orders them, so a test can
// prove that a closed peek leaves neither of the others run.
type sessionIdentitySteps struct {
	// relaunchClosed is the relaunch's peek; nil for any other launch.
	relaunchClosed   func() bool
	registerPresence func()
	mint             func()
}

// identitySteps builds the steps: the peek only for a restart's relaunch,
// and not under --print-launch-plan.
func identitySteps(relaunched, printLaunchPlan bool, peekClosed func() bool, registerPresence, mint func()) sessionIdentitySteps {
	s := sessionIdentitySteps{registerPresence: registerPresence, mint: mint}
	if relaunched && !printLaunchPlan {
		s.relaunchClosed = peekClosed
	}
	return s
}

// run runs the steps in order and reports whether the launch goes on: false
// only when the relaunch's peek found the session closed, and then neither
// the presence registration nor the mint ran.
func (s sessionIdentitySteps) run() bool {
	if s.relaunchClosed != nil && s.relaunchClosed() {
		return false
	}
	s.registerPresence()
	s.mint()
	return true
}

// restartedNotice is the system-prompt fragment telling a resumed session it
// was restarted, and why. The reason comes from whoever asked spinclass, so it
// is reduced to one line of printable text and bounded.
func restartedNotice(reason string) string {
	clean := sanitizeRestartReason(reason)
	if clean == "" {
		return ""
	}
	return "restarted: " + clean + "\n\nThis session was stopped and relaunched by its clown frontend at spinclass's request (spinclass#419). The conversation is the same session under the same principal; its tools and configuration were loaded afresh and may have changed."
}

// restartFallbackReason is the relaunch's reason when spinclass's verdict
// gives none.
const restartFallbackReason = "requested through spinclass"

// frontendRestarter performs a restart the preflight allowed.
type frontendRestarter struct {
	principal string
	argv      []string
	// stop ends the provider; runWithFlags then returns to main.
	stop func()
	// countdown runs before the stop and may cancel the restart; nil
	// restarts at once.
	countdown func(reason string, cancelOnTurn bool) (proceed bool, how string)
	logPath   string
	// profileCheck dry-runs the relaunch's profile gate before anything is
	// stopped (restartProfileCheck); nil skips it.
	profileCheck func() error
	// busy reports a turn running (the UserPromptSubmit hook); nil: never.
	busy func() bool
	// stopGrace bounds the wait for the provider to exit after the stop;
	// zero: defaultStopGrace.
	stopGrace time.Duration

	// kill SIGKILLs the provider after stopGrace; nil: killProvider.
	kill func() error
	// graceChecked, when set, is told whether the grace check killed (tests).
	graceChecked func(killed bool)
	// worktree is the session's working directory, checked before the
	// relaunch (relaunchPlan.worktree).
	worktree string
	// notify gets one line when a restart the preflight consumed is
	// cancelled (the frontend's stderr); nil: the log only.
	notify io.Writer
}

// cancelled records a restart dropped after spinclass consumed the request:
// nothing will retry it, so the operator and the log are told the requester
// must ask again.
func (fr frontendRestarter) cancelled(why string) {
	countMetric(metricRestartCancelled)
	why = printableOneLine(why)
	logSelfClose(fr.logPath, "restart (%s) cancelled after spinclass consumed the request: %s; the requester must ask again", fr.principal, why)
	if fr.notify != nil {
		fmt.Fprintf(fr.notify, "\r\nclown: restart cancelled (%s); its requester must ask again\r\n", why)
	}
}

func (fr frontendRestarter) restart(req restartRequest, v preflightVerdict) {
	if !providerRunning.Load() {
		logSelfClose(fr.logPath, "restart (%s): no provider is running; not restarting", fr.principal)
		return
	}
	if fr.profileCheck != nil {
		if err := fr.profileCheck(); err != nil {
			fr.cancelled(fmt.Sprintf("the relaunch's profile gate would refuse, so the session keeps running: %v", err))
			return
		}
	}
	// spinclass's own reason, from the verdict. The queued one came over the
	// control socket, which any same-uid process can write, so it is not
	// used.
	reason := cmpOr(sanitizeRestartReason(v.Reason), restartFallbackReason)
	// Under spinclass's `now` verdict a running turn is expected (the stop
	// kills it, which is the point), so a turn start cancels nothing; under
	// any other the busy cancel applies. The verdict decides, not the
	// request: the control socket's `when` only made the frontend ask sooner.
	cancelOnTurn := v.When != "now"
	if fr.countdown != nil {
		proceed, how := fr.countdown(reason, cancelOnTurn)
		logSelfClose(fr.logPath, "restart (%s): countdown: %s", fr.principal, how)
		if !proceed {
			fr.cancelled(how)
			return
		}
	}
	// Re-checked after the countdown: a turn that started during it is not
	// stopped mid-turn. The request was consumed by the preflight, so the
	// restart is dropped; the requester asks again.
	if cancelOnTurn && fr.busy != nil && fr.busy() {
		fr.cancelled("a turn started during the countdown")
		return
	}
	// The plan is stored only while the provider still runs, checked under
	// the lock its exit takes: a provider that exited (during the countdown or
	// just now) gets no plan, so main does not relaunch a session that ended.
	plan := &relaunchPlan{principal: fr.principal, reason: reason, argv: fr.argv, worktree: fr.worktree}
	if !storePlanIfRunning(plan) {
		logSelfClose(fr.logPath, "restart (%s): the provider exited during the countdown; not restarting", fr.principal)
		return
	}
	countMetric(metricRestartStopping)
	logSelfClose(fr.logPath, "restart (%s): stopping the provider to relaunch (%s)", fr.principal, reason)
	fr.stop()
	// A provider that ignores the SIGTERM is killed after the grace period.
	// The plan stays: whenever the provider does exit, main relaunches.
	grace := fr.stopGrace
	if grace == 0 {
		grace = defaultStopGrace
	}
	kill := fr.kill
	if kill == nil {
		kill = killProvider
	}
	go func() {
		time.Sleep(grace)
		killed := false
		if providerRunning.Load() {
			logSelfClose(fr.logPath, "restart (%s): the provider did not exit within %s of the stop; sending it SIGKILL", fr.principal, grace)
			if err := kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				logSelfClose(fr.logPath, "restart (%s): SIGKILL failed: %v", fr.principal, err)
			}
			killed = true
		}
		if fr.graceChecked != nil {
			fr.graceChecked(killed)
		}
	}()
}

// defaultStopGrace is how long a stopped provider has to exit before it is
// killed.
const defaultStopGrace = 30 * time.Second

// stopProviderBySignal ends the provider the way an external SIGTERM to clown
// does: every runProvider path (direct, pty-suspend, agent scope) forwards it
// to the provider, and the agent unit is stopped on the way out.
func stopProviderBySignal() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }

// relauncher re-execs the frontend for a relaunchPlan.
type relauncher struct {
	runtimeDir string
	environ    []string
	lookPath   func(string) (string, error)
	exec       func(path string, argv, env []string) error
	stderr     io.Writer
	// serveWait bounds the wait for the old serve socket.
	serveWait time.Duration
	// logPath is close-self-run.log, where every outcome is recorded.
	logPath string
	// spinclass is the spinclass binary asked `restart-preflight --peek`
	// before the exec; "" skips the peek.
	spinclass string
	// peekTimeout bounds the peek.
	peekTimeout time.Duration
}

func newRelauncher() relauncher {
	return relauncher{
		runtimeDir: os.Getenv("XDG_RUNTIME_DIR"),
		environ:    launchEnviron,
		lookPath:   exec.LookPath,
		exec:       syscall.Exec,
		stderr:     os.Stderr,
		serveWait:  15 * time.Second,
		logPath:    relaunchLogPath(),
		spinclass:  lookPathOrEmpty("spinclass"),
	}
}

func lookPathOrEmpty(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// relaunchLogPath is close-self-run.log, "" when the state dir is unknown.
func relaunchLogPath() string {
	p, err := userStateWritePath("close-self-run.log")
	if err != nil {
		return ""
	}
	return p
}

// relaunch waits until no serve socket of the principal answers (a relaunched
// bridge refuses a live one, fdda002), then execs `clown` from PATH, so a new
// clown build is picked up. It returns only when it does not relaunch, with
// the exit code.
//
// A session closed while the restart waited is not relaunched: that would
// resurrect it and re-mint a revoked principal. spinclass removes the
// worktree last on close (after stopping the unit and revoking), so the
// check runs as late as it can, immediately before the exec; it is best
// effort, and a close that has not yet reached the worktree removal by then
// is missed. An exit the user asked for in that window (/exit) is not told
// apart from the stop's and does relaunch. Every outcome is also logged.
func (r relauncher) relaunch(plan *relaunchPlan) int {
	fail := func(code int, format string, args ...any) int {
		countMetric(metricRelaunchAborted)
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(r.stderr, "clown: restart: %s\n", msg)
		logSelfClose(r.logPath, "relaunch (%s): %s", plan.principal, msg)
		return code
	}
	if r.runtimeDir != "" {
		dir := filepath.Join(r.runtimeDir, "clown", "serve", plan.principal)
		if err := waitServeSocketsGone(dir, r.serveWait); err != nil {
			return fail(1, "%v; not relaunching (resume with `clown resume %s`)", err, plan.principal)
		}
	}
	bin, err := r.lookPath("clown")
	if err != nil {
		return fail(1, "no clown on PATH: %v; not relaunching (resume with `clown resume %s`)", err, plan.principal)
	}
	// spinclass's own word first: `restart-preflight --peek` reads the
	// session without consuming anything, and reports a close that has only
	// started (ClosingAt) as closed. Anything but closed or live is unknown,
	// which relaunches; a spinclass without --peek exits non-zero.
	if r.spinclass != "" {
		state, err := peekSessionState(r.spinclass, plan.principal, r.environ, r.peekTimeout)
		switch {
		case err != nil:
			logSelfClose(r.logPath, "relaunch (%s): session state unknown (%v); relaunching on the worktree check", plan.principal, err)
		case state == "closed":
			return fail(0, "spinclass reports the session closed; not relaunching")
		default:
			logSelfClose(r.logPath, "relaunch (%s): spinclass reports the session %s", plan.principal, state)
		}
	} else {
		logSelfClose(r.logPath, "relaunch (%s): spinclass is not on PATH, session state unknown; relaunching on the worktree check", plan.principal)
	}
	// Fallback: spinclass removes the worktree on close (last).
	if plan.worktree != "" {
		if fi, err := os.Stat(plan.worktree); err != nil || !fi.IsDir() {
			return fail(0, "the session's worktree %s is gone, so the session was closed during the restart; not relaunching", plan.worktree)
		}
	}
	countMetric(metricRelaunchExec)
	logSelfClose(r.logPath, "relaunch (%s): relaunching: exec %s", plan.principal, bin)
	err = r.exec(bin, append([]string{bin}, plan.argv...), restartEnv(r.environ, plan))
	return fail(1, "exec %s: %v; not relaunched (resume with `clown resume %s`)", bin, err, plan.principal)
}

// defaultPeekTimeout bounds `spinclass restart-preflight --peek`.
const defaultPeekTimeout = 20 * time.Second

// peekSessionState runs `spinclass restart-preflight --peek` with the
// principal in CLOWN_SESSION_ID and returns its state, "closed" or "live".
// A non-zero exit, a timeout, output that is not exactly one JSON line, or
// any other state is an error: the caller treats it as unknown.
func peekSessionState(bin, principal string, environ []string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = defaultPeekTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "restart-preflight", "--peek")
	// The launch environment minus the keys the relaunch drops
	// (restartEnvDropped), plus the principal; not clown's current
	// environment, which runWithFlags has added to. Unlike the relaunch, no
	// restart notice is added.
	cmd.Env = append(environWithout(environ, restartEnvDropped...), "CLOWN_SESSION_ID="+principal)
	// Its own process group; on timeout only, the whole group is killed, so
	// nothing it started outlives the peek. WaitDelay is not for I/O (stdout
	// is a file, so Wait has no copy goroutines): if the group kill failed,
	// it makes Go kill the direct child after a second, so Wait still ends.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	var cancelErr error
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			cancelErr = err
		}
		return nil
	}
	out, err := outputViaFile(cmd)
	if ctx.Err() != nil {
		if cancelErr != nil {
			return "", fmt.Errorf("restart-preflight --peek timed out after %s, and killing its process group failed: %v", timeout, cancelErr)
		}
		return "", fmt.Errorf("restart-preflight --peek timed out after %s", timeout)
	}
	if err != nil {
		return "", fmt.Errorf("restart-preflight --peek: %w", err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" || strings.Contains(line, "\n") {
		return "", fmt.Errorf("restart-preflight --peek: want one JSON line, got %q", line)
	}
	var v struct {
		Verdict string `json:"verdict"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return "", fmt.Errorf("restart-preflight --peek: %w", err)
	}
	// A peek never restarts anything: its verdict is always none.
	if v.Verdict != "none" {
		return "", fmt.Errorf("restart-preflight --peek: verdict %q, want none", v.Verdict)
	}
	if v.State != "closed" && v.State != "live" {
		return "", fmt.Errorf("restart-preflight --peek: state %q", v.State)
	}
	return v.State, nil
}

// waitServeSocketsGone polls dir until none of its sockets accepts a
// connection, or timeout passes. A missing directory has none.
func waitServeSocketsGone(dir string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		live := liveSockets(dir)
		if len(live) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("serve socket %s is still live after %s", live[0], timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func liveSockets(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var live []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if c, err := net.DialTimeout("unix", p, 200*time.Millisecond); err == nil {
			c.Close()
			live = append(live, p)
		}
	}
	slices.Sort(live)
	return live
}

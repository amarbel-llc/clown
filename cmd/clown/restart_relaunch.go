package main

import (
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
	providerKill func()
	providerMu   sync.Mutex
)

// restartUnderway reports that a restart has recorded its plan: from then
// until the relaunch, the idle poke runs nothing and the control socket
// refuses another restart.
func restartUnderway() bool { return pendingRelaunch.Load() != nil }

// setProviderRunning marks the provider running (its SIGTERM forwarding is
// in place) with how to kill it.
func setProviderRunning(kill func()) {
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
// as running: the check and the kill hold the lock clearProviderRunning takes
// when runProvider returns.
func killProvider() {
	providerMu.Lock()
	defer providerMu.Unlock()
	if providerRunning.Load() && providerKill != nil {
		providerKill()
	}
}

// restartArgv is this launch's argv for the relaunch: the resolved flags
// (reexecArgv, as the [attach] re-exec uses), with any --session-id/--resume
// replaced by --resume <principal>, and the [attach] id kept for the inner
// clown of a multiplexer so it does not wrap again.
func restartArgv(flags parsedFlags, attachedID string) []string {
	f := flags
	f.forwarded = append(withoutClaudeSessionArgs(flags.forwarded), "--resume", flags.identity.Key)
	var argv []string
	if attachedID != "" {
		argv = append(argv, attachIDFlag, attachedID)
	}
	return append(argv, f.reexecArgv()...)
}

// withoutClaudeSessionArgs drops claude's --session-id and --resume/-r (each
// with its value, in either form).
func withoutClaudeSessionArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--session-id" || a == "--resume" || a == "-r":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(a, "--session-id=") || strings.HasPrefix(a, "--resume="):
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

// restartEnv is the relaunch's environment: the launch environment with the
// principal in CLOWN_SESSION_ID and the notice in CLOWN_RESTARTED_REASON. The
// launcher's one-shot profile approval is not replayed (the argv carries
// --profile, and the session is already pinned), and no other session id
// survives.
func restartEnv(environ []string, plan *relaunchPlan) []string {
	drop := []string{"CLOWN_SESSION_ID", "CLAUDE_SESSION_ID", "CLOWN_PROFILE", "CLOWN_PROFILE_DIGEST", "CLOWN_PROFILE_SESSION", restartedReasonEnv}
	env := environWithout(environ, drop...)
	return append(env, "CLOWN_SESSION_ID="+plan.principal, restartedReasonEnv+"="+plan.reason)
}

// restartedReasonEnv carries the restart's reason to the relaunched frontend,
// which consumes it (consumeRestartedNotice) before starting anything.
const restartedReasonEnv = "CLOWN_RESTARTED_REASON"

// consumeRestartedNotice reads and unsets CLOWN_RESTARTED_REASON, returning
// the prompt notice for a relaunched session ("" for any other launch).
func consumeRestartedNotice() string {
	reason, ok := os.LookupEnv(restartedReasonEnv)
	if !ok {
		return ""
	}
	_ = os.Unsetenv(restartedReasonEnv)
	if reason == "" {
		reason = "no reason given"
	}
	return restartedNotice(reason)
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
	countdown func(reason string) (proceed bool, how string)
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
	kill func()
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
	why = strings.Join(strings.Fields(why), " ")
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
	if fr.countdown != nil {
		proceed, how := fr.countdown(reason)
		logSelfClose(fr.logPath, "restart (%s): countdown: %s", fr.principal, how)
		if !proceed {
			fr.cancelled(how)
			return
		}
	}
	// Re-checked after the countdown: a turn that started during it is not
	// stopped mid-turn. The request was consumed by the preflight, so the
	// restart is dropped; the requester asks again.
	if fr.busy != nil && fr.busy() {
		fr.cancelled("a turn started during the countdown")
		return
	}
	// The provider may have exited during the countdown: nothing to stop,
	// and a plan stored now would relaunch a session that ended.
	if !providerRunning.Load() {
		logSelfClose(fr.logPath, "restart (%s): the provider exited during the countdown; not restarting", fr.principal)
		return
	}
	plan := &relaunchPlan{principal: fr.principal, reason: reason, argv: fr.argv, worktree: fr.worktree}
	pendingRelaunch.Store(plan)
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
			kill()
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
}

func newRelauncher() relauncher {
	return relauncher{
		runtimeDir: os.Getenv("XDG_RUNTIME_DIR"),
		environ:    launchEnviron,
		lookPath:   exec.LookPath,
		exec:       syscall.Exec,
		stderr:     os.Stderr,
		serveWait:  15 * time.Second,
	}
}

// relaunch waits until no serve socket of the principal answers (a relaunched
// bridge refuses a live one, fdda002), then execs `clown` from PATH, so a new
// clown build is picked up. It returns only when it does not relaunch, with
// the exit code.
//
// A session closed while the restart waited (spinclass removes its
// worktree on close) is not relaunched: that would resurrect it and re-mint
// a revoked principal. An exit the user asked for in that window (/exit) is
// not told apart from the stop's and does relaunch.
func (r relauncher) relaunch(plan *relaunchPlan) int {
	if plan.worktree != "" {
		if fi, err := os.Stat(plan.worktree); err != nil || !fi.IsDir() {
			fmt.Fprintf(r.stderr, "clown: restart: the session's worktree %s is gone, so the session was closed during the restart; not relaunching\n", plan.worktree)
			return 0
		}
	}
	if r.runtimeDir != "" {
		dir := filepath.Join(r.runtimeDir, "clown", "serve", plan.principal)
		if err := waitServeSocketsGone(dir, r.serveWait); err != nil {
			fmt.Fprintf(r.stderr, "clown: restart: %v; not relaunching (resume with `clown resume %s`)\n", err, plan.principal)
			return 1
		}
	}
	bin, err := r.lookPath("clown")
	if err != nil {
		fmt.Fprintf(r.stderr, "clown: restart: no clown on PATH: %v; not relaunching (resume with `clown resume %s`)\n", err, plan.principal)
		return 1
	}
	err = r.exec(bin, append([]string{bin}, plan.argv...), restartEnv(r.environ, plan))
	fmt.Fprintf(r.stderr, "clown: restart: exec %s: %v; not relaunched (resume with `clown resume %s`)\n", bin, err, plan.principal)
	return 1
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

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
}

var (
	// pendingRelaunch is set by the idle runner just before it stops the
	// provider; main relaunches when it is set.
	pendingRelaunch atomic.Pointer[relaunchPlan]
	// providerRunning is true while runProvider waits on the provider, the
	// only time stopping it unwinds to main.
	providerRunning atomic.Bool
)

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
	if reason == "" {
		return ""
	}
	clean := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, reason)), " ")
	if r := []rune(clean); len(r) > 300 {
		clean = string(r[:300]) + "..."
	}
	return "restarted: " + clean + "\n\nThis session was stopped and relaunched by its clown frontend at spinclass's request (spinclass#419). The conversation is the same session under the same principal; its tools and configuration were loaded afresh and may have changed."
}

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
}

func (fr frontendRestarter) restart(req restartRequest, v preflightVerdict) {
	if !providerRunning.Load() {
		logSelfClose(fr.logPath, "restart (%s): no provider is running; not restarting", fr.principal)
		return
	}
	reason := cmpOr(req.Reason, v.Reason)
	if fr.countdown != nil {
		proceed, how := fr.countdown(reason)
		logSelfClose(fr.logPath, "restart (%s): countdown: %s", fr.principal, how)
		if !proceed {
			return
		}
	}
	pendingRelaunch.Store(&relaunchPlan{principal: fr.principal, reason: reason, argv: fr.argv})
	logSelfClose(fr.logPath, "restart (%s): stopping the provider to relaunch (%s)", fr.principal, reason)
	fr.stop()
}

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
// clown build is picked up. It returns only on failure, with the exit code.
func (r relauncher) relaunch(plan *relaunchPlan) int {
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
	fmt.Fprintf(r.stderr, "clown: restart: exec %s: %v\n", bin, err)
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

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"code.linenisgreat.com/clown/internal/sessionstatus"
	"code.linenisgreat.com/clown/internal/staging"
)

// A session ends itself in two halves (spinclass#389): its close-this-session
// tool only records a request, and `spinclass close-self-run` does the teardown,
// which stops the agent unit and so cannot run inside it. clown#287 is the
// trigger: a Stop hook in the agent pokes the frontend over a unix datagram
// socket, and the frontend — outside the unit — runs close-self-run. The runner
// is a no-op without a request and re-checks everything itself, so poking it on
// every idle transition is safe.
//
// The poke is not authenticated: the socket is bound into the unit, and
// anything there running as the session's uid (the agent's own Bash, not only
// the hooks) can send it, with no way to tell the two apart. Forging one runs
// close-self-run (a no-op without a request spinclass recorded) and, when
// `clown restart` queued a restart, the preflight, whose verdict still gates
// it: a forged poke cannot start a restart spinclass did not authorize. What it
// can do is choose when an already-authorized restart happens: a poke clears
// the turn-running mark, so the agent can let a queued restart proceed while
// it is mid-turn (and a forged turn-start byte can cancel one).

// idleSocketPath is where the frontend listens for idle pokes.
func idleSocketPath(runtimeDir, key string) string {
	return filepath.Join(runtimeDir, "clown", "idle", key+".sock")
}

// selfCloseArmed reports whether this launch should listen for idle pokes, and
// the spinclass binary to run. It needs a claude launch (the Stop hook is a
// plugin hook) inside a spinclass session, a runtime dir for the socket and
// spinclass on PATH. --print-launch-plan never arms: it writes no state.
func selfCloseArmed(flags parsedFlags) (spinclassPath string, ok bool) {
	path, reason := selfCloseDecision(flags)
	return path, reason == ""
}

// selfCloseDecision is selfCloseArmed with the reason it declined: an empty
// reason means armed. It names the failing gate so a silent launch is
// diagnosable.
func selfCloseDecision(flags parsedFlags) (spinclassPath, reason string) {
	switch {
	case flags.provider != "claude":
		return "", "provider is not claude"
	case flags.naked:
		return "", "--naked"
	case flags.printLaunchPlan:
		return "", "--print-launch-plan"
	case flags.identity.Key == "":
		return "", "no session identity key"
	case os.Getenv("CLOWN_DISABLE_SELF_CLOSE") == "1":
		return "", "CLOWN_DISABLE_SELF_CLOSE=1"
	case os.Getenv("SPINCLASS_SESSION_ID") == "":
		return "", "SPINCLASS_SESSION_ID is not set in clown's environment"
	case os.Getenv("XDG_RUNTIME_DIR") == "":
		return "", "XDG_RUNTIME_DIR is not set in clown's environment"
	}
	path, err := exec.LookPath("spinclass")
	if err != nil {
		return "", "spinclass is not on clown's PATH"
	}
	return path, ""
}

// logSelfClose appends one timestamped line to the close-self-run log, best
// effort: the log is the only record of why a launch did or did not arm.
var (
	// logWarnings gets the one "cannot write" line per process.
	logWarnings io.Writer = os.Stderr
	logWarnOnce           = newOnce()
)

func newOnce() *sync.Once { return &sync.Once{} }

func logSelfClose(logPath, format string, args ...any) {
	if logPath == "" {
		return
	}
	f, err := openLogForAppend(logPath)
	if err != nil {
		// Once per process: this runs on every idle transition, and stderr is
		// claude's TUI.
		logWarnOnce.Do(func() {
			fmt.Fprintf(logWarnings, "clown: cannot write %s: %v (further log failures are not reported)\n", logPath, err)
		})
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// openLogForAppend opens a log for appending (0600, its directory 0700).
// O_NOFOLLOW: idle-notify's log has a fixed name in a temp dir, where a
// planted symlink must not redirect the append. O_NONBLOCK: a FIFO planted
// there must not block the serial idle runner on open.
func openLogForAppend(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
}

// selfCloseRunner runs `spinclass close-self-run` for one session.
type selfCloseRunner struct {
	bin       string
	principal string
	logPath   string
}

// run starts close-self-run with the principal in CLOWN_SESSION_ID and waits for
// it. A non-zero exit is spinclass refusing (it has already told the holders),
// so it is logged, not returned; only failing to start is an error.
//
// The runner gets its own session so it outlives the frontend: the teardown it
// does stops the unit, which ends this process. Its output goes to a log file,
// never the terminal claude owns, and never a pipe the exiting frontend would
// break under it.
func (r *selfCloseRunner) run() error {
	log, err := openLogForAppend(r.logPath)
	if err != nil {
		logWarnOnce.Do(func() {
			fmt.Fprintf(logWarnings, "clown: cannot write %s: %v; close-self-run not run (further log failures are not reported)\n", r.logPath, err)
		})
		return err
	}
	defer log.Close()
	fmt.Fprintf(log, "%s close-self-run %s\n", time.Now().UTC().Format(time.RFC3339), r.principal)

	cmd := exec.Command(r.bin, "close-self-run")
	cmd.Env = append(os.Environ(), "CLOWN_SESSION_ID="+r.principal)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(log, "start: %v\n", err)
		return err
	}
	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(log, "%v\n", err)
	}
	return nil
}

// idleListener receives idle pokes on a unixgram socket and runs a function
// once per poke, serially. Pokes that arrive during a run collapse into one
// more run: spinclass's runner is not safe to overlap.
type idleListener struct {
	conn  *net.UnixConn
	path  string
	bound os.FileInfo
	wake  chan struct{}
	// done closes once the run goroutine has drained every queued wake after
	// Close.
	done chan struct{}
	once sync.Once
}

// turnStartedDatagram is what `clown idle-notify --busy` sends from the
// UserPromptSubmit hook: it marks a turn running and wakes nothing. It may be
// followed by a payload (sessionstatus.Payload as JSON, at most
// sessionstatus.MaxPayload bytes) when the hook command carries
// idleNotifyPayloadFlag. Every datagram not starting with it is an idle poke.
//
// An older frontend reads only a one-byte 'B' as a turn start and anything
// longer as an idle poke, so idle-notify sends a payload only when the hook
// command, which this frontend wrote, says it understands one.
const turnStartedDatagram = 'B'

// idleNotifyPayloadFlag, an argument of the busy hook's command line
// (`idle-notify --busy --payload SOCK`), tells idle-notify that the frontend
// that wrote the hook accepts a payload. It is an argument, not an
// environment prefix, so the command does not depend on which shell claude
// runs hooks through (the operator's is fish).
const idleNotifyPayloadFlag = "--payload"

func startIdleListener(path string, run func()) (*idleListener, error) {
	return startIdleListenerTracking(path, nil, nil, run)
}

// startIdleListenerTracking is startIdleListener that also records, in busy
// (nil: not recorded), whether a turn is running: set by a turn-start
// datagram, cleared by the next idle poke. Anything in the unit can send
// either (see the top of this file): a forged turn start cancels a restart
// counting down, a forged poke lets an authorized one proceed mid-turn. Every
// datagram is also written to status (nil: no status file), from this
// listener's reader goroutine; a forged datagram forges the status the same
// way, which the status file's contract states.
func startIdleListenerTracking(path string, busy *atomic.Bool, status *sessionstatus.Writer, run func()) (*idleListener, error) {
	if err := checkUnixSocketPath(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// A socket another listener still has bound (connect succeeds on a bound
	// unixgram socket) belongs to a live frontend of the same key: refused,
	// not taken over. Only a stale file is replaced.
	if c, err := net.DialTimeout("unixgram", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("idle socket %s is live: another clown frontend serves this key", path)
	}
	_ = os.Remove(path)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		conn.Close()
		_ = os.Remove(path)
		return nil, err
	}
	bound, err := os.Stat(path)
	if err != nil {
		conn.Close()
		_ = os.Remove(path)
		return nil, err
	}
	l := &idleListener{conn: conn, path: path, bound: bound, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		// One byte more than the largest datagram idle-notify sends: a
		// datagram that fills it was truncated, and its payload is dropped.
		buf := make([]byte, 1+sessionstatus.MaxPayload+1)
		for {
			n, _, err := conn.ReadFromUnix(buf)
			if err != nil {
				close(l.wake)
				return
			}
			if n >= 1 && buf[0] == turnStartedDatagram {
				if busy != nil {
					busy.Store(true)
				}
				var payload sessionstatus.Payload
				if n > 1 && n < len(buf) {
					payload, _ = sessionstatus.ParsePayload(buf[1:n])
				}
				status.TurnStarted(payload)
				continue
			}
			if busy != nil {
				busy.Store(false)
			}
			status.TurnEnded()
			select {
			case l.wake <- struct{}{}:
			default:
			}
		}
	}()
	go func() {
		defer close(l.done)
		for range l.wake {
			run()
		}
	}()
	return l, nil
}

// Close stops listening and removes the socket. It does not wait for a run in
// flight: close-self-run runs in its own session and outlives the frontend;
// a restart in flight has stored its plan, and while it is stored the idle
// runner starts nothing (restartUnderway), so the exec cannot cut a close or
// a second restart in half.
func (l *idleListener) Close() {
	l.once.Do(func() {
		l.conn.Close()
		// Only while the path is still this listener's socket, not a
		// successor's.
		if fi, err := os.Stat(l.path); err == nil && os.SameFile(fi, l.bound) {
			_ = os.Remove(l.path)
		}
	})
}

// runIdleNotify is `clown idle-notify SOCKET`, the Stop hook's command: it
// sends one datagram to the frontend and always exits 0, so it can never fail
// or stall the agent's turn.
//
// A hook has no other voice, so its outcome is appended to
// clown-idle-notify.log in the temp dir (inside an agent unit that is the
// worktree's .tmp, which the operator can read).
func runIdleNotify(args []string) int {
	if len(args) == 3 && args[0] == "--busy" && args[1] == idleNotifyPayloadFlag {
		return idleNotify([]string{args[0], args[2]}, os.Stdin, true)
	}
	return idleNotify(args, os.Stdin, false)
}

// shellSingleQuote quotes s for a POSIX shell or fish: single quotes, with an
// embedded quote written as '\” (close, escaped quote, reopen), which both
// shells read the same way.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// idleNotify is runIdleNotify with its inputs explicit. With payload set (the
// frontend flagged the busy hook), the busy datagram carries the hook's
// transcript path and session id, read from stdin by readHookPayload.
func idleNotify(args []string, stdin io.Reader, payload bool) int {
	record := filepath.Join(os.TempDir(), "clown-idle-notify.log")
	datagram := []byte{1}
	busy := len(args) == 2 && args[0] == "--busy"
	if busy {
		datagram, args = []byte{turnStartedDatagram}, args[1:]
	}
	if len(args) != 1 {
		logSelfClose(record, "idle-notify: want one socket argument, got %d", len(args))
		return 0
	}
	if busy && payload {
		datagram = append(datagram, readHookPayload(stdin, hookStdinDeadline)...)
	}
	conn, err := net.DialTimeout("unixgram", args[0], 250*time.Millisecond)
	if err != nil {
		logSelfClose(record, "idle-notify: dial failed: %v", err)
		return 0
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
	if _, err := conn.Write(datagram); err != nil {
		logSelfClose(record, "idle-notify: write failed: %v", err)
		return 0
	}
	logSelfClose(record, "idle-notify: sent to %s", args[0])
	return 0
}

// The busy hook reads its stdin for at most hookStdinDeadline and
// hookStdinLimit bytes: a hook must never stall the turn, and the payload
// claude sends carries the whole prompt after the fields wanted here.
const (
	hookStdinDeadline = 200 * time.Millisecond
	hookStdinLimit    = 1 << 20
)

// readHookPayload returns the transcript path and session id from a hook's
// stdin as a datagram payload, or nil when stdin is a terminal, absent, slow,
// not JSON, or names neither. Only those two fields are sent: the prompt never
// leaves the hook.
func readHookPayload(stdin io.Reader, deadline time.Duration) []byte {
	if stdin == nil {
		return nil
	}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return nil
	}
	found := make(chan sessionstatus.Payload, 1)
	// On a timeout this reader is abandoned; idle-notify exits right after.
	go func() { found <- scanHookFields(io.LimitReader(stdin, hookStdinLimit)) }()
	var p sessionstatus.Payload
	select {
	case p = <-found:
	case <-time.After(deadline):
		return nil
	}
	if p == (sessionstatus.Payload{}) {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	if _, ok := sessionstatus.ParsePayload(b); !ok {
		return nil
	}
	return b
}

// scanHookFields walks the top-level object on r and keeps transcript_path and
// session_id, stopping as soon as it has both, so a large prompt after them is
// never read. Whatever it found before an error is returned.
func scanHookFields(r io.Reader) sessionstatus.Payload {
	var p sessionstatus.Payload
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return p
	}
	var haveTranscript, haveSession bool
	for dec.More() && !(haveTranscript && haveSession) {
		t, err := dec.Token()
		if err != nil {
			return p
		}
		key, _ := t.(string)
		switch key {
		case "transcript_path":
			if dec.Decode(&p.TranscriptPath) != nil {
				return p
			}
			haveTranscript = true
		case "session_id":
			if dec.Decode(&p.SessionID) != nil {
				return p
			}
			haveSession = true
		default:
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return p
			}
		}
	}
	return p
}

// synthIdlePluginDir writes a built-in plugin directory whose hooks.json runs
// `clown idle-notify SOCK` on Stop, and returns its path. The staging root owns
// the directory.
func synthIdlePluginDir(root *staging.Root, clownBin, sock string) (string, error) {
	dir, err := root.Dir("clown-selfclose-plugin-*")
	if err != nil {
		return "", err
	}
	manifestDir := filepath.Join(dir, ".claude-plugin")
	hooksDir := filepath.Join(dir, "hooks")
	for _, d := range []string{manifestDir, hooksDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	manifest, err := json.MarshalIndent(map[string]any{"name": "clown-selfclose", "version": "1"}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), manifest, 0o600); err != nil {
		return "", err
	}
	hook := func(command string) []any {
		return []any{map[string]any{"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
			"timeout": 5,
		}}}}
	}
	// Stop pokes the frontend at idle; UserPromptSubmit tells it a turn
	// started, so a restart counting down does not stop the agent mid-turn,
	// and, flagged by idleNotifyPayloadFlag, which transcript the turn writes
	// (the status file, clown#322). Paths are quoted: the command is a shell
	// command line of unknown shell.
	exe, qsock := shellSingleQuote(clownBin), shellSingleQuote(sock)
	hooks, err := json.MarshalIndent(map[string]any{"hooks": map[string]any{
		"Stop":             hook(exe + " idle-notify " + qsock),
		"UserPromptSubmit": hook(exe + " idle-notify --busy " + idleNotifyPayloadFlag + " " + qsock),
	}}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), hooks, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// armSelfClose starts the idle listener and registers the Stop hook for a
// launch that selfCloseArmed allows, returning the plugin dir to add and the
// socket to bind into an agent unit ("" when not armed or on any failure: a
// session that cannot self-close is still a working session). Each idle poke
// runs close-self-run and then, when restarts holds one, the restart preflight
// (idleRunner); restart performs an allowed restart. restarts may be nil.
// now is the runner's immediate trigger for `clown restart --when now`
// (idleRunner.triggerNow), nil when not armed or restarts is nil. status is
// the session's status file (clown#322), written at arming and on every
// datagram; the caller adds the unit and provider pid when they are known.
// It is nil when not armed; closeFn removes the file.
func armSelfClose(flags parsedFlags, root *staging.Root, restarts *restartQueue, restart func(restartRequest, preflightVerdict), busy *atomic.Bool) (pluginDir, sock string, trig *restartTriggers, status *sessionstatus.Writer, closeFn func()) {
	bin, reason := selfCloseDecision(flags)
	logPath, logErr := userStateWritePath("close-self-run.log")
	notArmed := func(why string) (string, string, *restartTriggers, *sessionstatus.Writer, func()) {
		fmt.Fprintf(os.Stderr, "clown: self-close not armed: %s\n", why)
		if logErr == nil {
			logSelfClose(logPath, "self-close not armed (%s): %s", flags.identity.Key, why)
		}
		return "", "", nil, nil, func() {}
	}
	if reason != "" {
		// Only a claude launch is a candidate; --naked, --print-launch-plan and
		// other providers are silent (a plan must write no state).
		if flags.provider == "claude" && !flags.naked && !flags.printLaunchPlan && logErr == nil {
			logSelfClose(logPath, "self-close not armed (%s): %s", flags.identity.Key, reason)
		}
		return "", "", nil, nil, func() {}
	}
	if logErr != nil {
		return notArmed(logErr.Error())
	}
	// A status file that cannot be written is logged and skipped: the
	// session works without it (a nil writer writes nothing).
	status, err := sessionstatus.Open(flags.identity.Key, os.Getenv("SPINCLASS_SESSION_ID"), func(format string, args ...any) {
		logSelfClose(logPath, format, args...)
	})
	if err != nil {
		logSelfClose(logPath, "status file not written (%s): %v", flags.identity.Key, err)
	}
	sock = idleSocketPath(os.Getenv("XDG_RUNTIME_DIR"), flags.identity.Key)
	idle := &idleRunner{
		closer:    &selfCloseRunner{bin: bin, principal: flags.identity.Key, logPath: logPath},
		preflight: &restartPreflight{bin: bin, principal: flags.identity.Key, logPath: logPath},
		queue:     restarts,
		restart:   restart,
		logPath:   logPath,
	}
	if busy != nil {
		idle.busy = busy.Load
	}
	l, err := startIdleListenerTracking(sock, busy, status, idle.onIdle)
	if err != nil {
		// The file is not touched: a live frontend of this key owns it.
		return notArmed(err.Error())
	}
	status.Start()
	unarm := func(why string) (string, string, *restartTriggers, *sessionstatus.Writer, func()) {
		status.Remove()
		l.Close()
		return notArmed(why)
	}
	// The hook runs THIS executable, never flags.identity.Bin: that honors
	// $CLOWN_BIN, which a devshell can point at an older clown that rejects
	// `idle-notify` and errors after every turn.
	exe := clownExePath()
	if exe == "" {
		return unarm("cannot resolve clown's own executable")
	}
	dir, err := synthIdlePluginDir(root, exe, sock)
	if err != nil {
		return unarm(err.Error())
	}
	logSelfClose(logPath, "self-close armed (%s): socket %s, spinclass %s, hook plugin %s, clown %s", flags.identity.Key, sock, bin, dir, exe)
	if restarts != nil {
		trig = &restartTriggers{now: idle.triggerNow, idle: idle.queueIdle}
	}
	return dir, sock, trig, status, func() {
		idle.noteExit()
		// The file goes before the socket: once the listener is closed a
		// successor frontend may bind and write its own file at this key.
		status.Remove()
		l.Close()
	}
}

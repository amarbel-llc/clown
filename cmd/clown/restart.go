package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// `clown restart` is the clown half of spinclass#419 (spinclass FDR 0037).
// spinclass authorizes and records a restart, then runs the operator's
// [session-entry].restart hand-off, `clown restart --session K --when idle
// --reason R`. That verb reaches the live frontend of K over a per-key control
// socket and queues the request; the frontend acts on it at the next idle
// transition, after close-self-run, by asking `spinclass restart-preflight`.
// Nothing here authorizes anything: a queued request only makes the frontend
// ask spinclass, which re-checks everything and consumes the request.

// controlSocketPath is where the frontend of key listens for `clown restart`,
// beside its idle socket.
func controlSocketPath(runtimeDir, key string) string {
	return filepath.Join(runtimeDir, "clown", "control", key+".sock")
}

// restartRequest is one queued restart. Posture is the hand-off's argument
// (clown#342), carried for the log; the verdict's posture is what the
// relaunch takes.
type restartRequest struct {
	When    string `json:"when"`
	Reason  string `json:"reason"`
	Posture string `json:"posture,omitempty"`
}

// controlMessage is the one JSON line `clown restart` writes.
type controlMessage struct {
	Op      string `json:"op"`
	When    string `json:"when"`
	Reason  string `json:"reason"`
	Posture string `json:"posture,omitempty"`
}

// controlReply is the one JSON line the frontend answers with.
type controlReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// restartQueue holds at most one pending restart: a newer request replaces an
// older one, and taking it empties the queue.
type restartQueue struct {
	mu      sync.Mutex
	pending *restartRequest

	// deferred and resuming hold a restart a turn start cancelled and its
	// retry in progress (restart_defer.go). clock is time.Now when nil.
	deferred, resuming *deferredRestart
	clock              func() time.Time
}

// put queues r, replacing a queued request and a deferred restart: a newer
// request is preflighted afresh.
func (q *restartQueue) put(r restartRequest) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = &r
	q.deferred = nil
}

func (q *restartQueue) hasPending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending != nil
}

func (q *restartQueue) take() (restartRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil {
		return restartRequest{}, false
	}
	r := *q.pending
	q.pending = nil
	return r, true
}

// restartControlHandler is restartControlHandlerNow for a frontend with no
// immediate trigger: it queues `idle` and refuses `now`.
func restartControlHandler(provider, unarmedReason string, q *restartQueue) func(controlMessage) controlReply {
	return restartControlHandlerNow(provider, unarmedReason, q, nil)
}

// restartAlreadyUnderway is the refusal for a request that arrives while a
// restart is stopping the provider, or while a preflight or restart runs
// (which may still end without a restart).
const restartAlreadyUnderway = "a restart or restart check is already underway for this session; ask again if the session does not relaunch"

// restartControlHandlerNow answers control messages for a frontend running
// provider. unarmedReason is selfCloseDecision's reason ("" when the idle
// trigger is armed): without the trigger nothing would ever act on a queued
// restart, so it is refused rather than queued.
//
// `idle` is queued for the next idle poke. `now` is handed to now
// (idleRunner.triggerNow), which queues it, replacing a queued `idle`, and
// runs the preflight at once in the background, so the reply comes back
// before the preflight; it reports false while a preflight or restart is
// already in flight, which is refused. Either way the request only makes the
// frontend ask spinclass: nothing restarts without spinclass's "restart"
// verdict. now is nil when the frontend has no immediate trigger.
func restartControlHandlerNow(provider, unarmedReason string, q *restartQueue, now func(restartRequest) bool) func(controlMessage) controlReply {
	return restartControlHandlerTriggers(provider, unarmedReason, q, &restartTriggers{now: now})
}

// restartControlHandlerTriggers is restartControlHandlerNow with the idle
// runner's triggers: `idle` goes to trig.idle, which queues it and, when the
// session is already idle, runs the idle transition at once (clown#327); with
// no idle trigger it is only queued for the next poke. trig may be nil.
func restartControlHandlerTriggers(provider, unarmedReason string, q *restartQueue, trig *restartTriggers) func(controlMessage) controlReply {
	var now func(restartRequest) bool
	var idle func(restartRequest)
	if trig != nil {
		now, idle = trig.now, trig.idle
	}
	return func(m controlMessage) controlReply {
		if m.Op != "restart" {
			return controlReply{Error: fmt.Sprintf("unknown control op %q", m.Op)}
		}
		if m.When != "idle" && m.When != "now" {
			return controlReply{Error: fmt.Sprintf("restart --when %q is not supported; use idle or now", m.When)}
		}
		if !validPosture(m.Posture) {
			return controlReply{Error: fmt.Sprintf("restart --posture %q is not supported; use unattended or attended", m.Posture)}
		}
		if restartUnderway() {
			return controlReply{Error: restartAlreadyUnderway}
		}
		if unarmedReason != "" {
			if provider != "claude" {
				return controlReply{Error: fmt.Sprintf("restart needs an idle trigger; %s has none", provider)}
			}
			return controlReply{Error: fmt.Sprintf("restart needs an idle trigger; %s's is not armed (%s)", provider, unarmedReason)}
		}
		// The relaunch's profile gate, at request time (clown#340): a request
		// the relaunch would refuse is not queued at all, so the requester
		// hears why now (spinclass clears the request and shows this), instead
		// of at the idle transition. The gate runs again before the stop.
		if trig != nil && trig.gate != nil {
			if err := trig.gate(); err != nil {
				countMetric(metricRestartRequestRefused)
				return controlReply{Error: restartGateRefusal(err)}
			}
		}
		// The relaunch's posture gate (clown#342), the same way: a session
		// that cannot take the requested posture is refused while it runs.
		if m.Posture != postureKeep && trig != nil && trig.posture != nil {
			if err := trig.posture(m.Posture); err != nil {
				countMetric(metricRestartRequestRefused)
				return controlReply{Error: restartPostureRefusal(m.Posture, err)}
			}
		}
		req := restartRequest{When: m.When, Reason: sanitizeRestartReason(m.Reason), Posture: m.Posture}
		if m.When == "idle" {
			if idle != nil {
				idle(req)
			} else {
				q.put(req)
			}
			return controlReply{OK: true}
		}
		if now == nil {
			return controlReply{Error: "restart --when now is not available in this frontend"}
		}
		if !now(req) {
			return controlReply{Error: restartAlreadyUnderway}
		}
		return controlReply{OK: true}
	}
}

// restartGateRefusal is the control reply, and the dropped-restart reason,
// for a relaunch the profile gate would refuse: the session keeps running.
func restartGateRefusal(err error) string {
	return "the relaunch's profile gate would refuse, so the session keeps running: " + err.Error()
}

// restartReasonMaxRunes caps a restart reason.
const restartReasonMaxRunes = 300

// sanitizeRestartReason reduces a restart reason to one line of printable
// text: control characters (C0, C1, newlines) become spaces, other
// non-printing runes (bidi controls, zero-width characters) are dropped,
// runs of space collapse, and it is capped at restartReasonMaxRunes runes,
// the "..." marking a cut included.
func sanitizeRestartReason(s string) string {
	clean := printableOneLine(s)
	if r := []rune(clean); len(r) > restartReasonMaxRunes {
		clean = string(r[:restartReasonMaxRunes-3]) + "..."
	}
	return clean
}

// printableOneLine is s as one line of printable text: control characters
// (C0, C1, newlines) become spaces, other non-printing runes (bidi controls,
// zero-width characters) are dropped, and runs of space collapse.
func printableOneLine(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r) || unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, s)), " ")
}

// controlListener serves the control socket.
type controlListener struct {
	ln   *net.UnixListener
	path string
	// bound is the socket file this listener created; Close removes path
	// only while it is still that file.
	bound os.FileInfo
	once  sync.Once
}

// unixSocketPathMax is sun_path's size, its terminating NUL included.
const unixSocketPathMax = 108

// checkUnixSocketPath refuses a path a unix socket cannot have (the control
// and idle sockets), so neither end mistakes it for a missing frontend.
func checkUnixSocketPath(path string) error {
	if len(path) >= unixSocketPathMax {
		return fmt.Errorf("socket path %s is too long for a unix socket (%d bytes, at most %d); shorten XDG_RUNTIME_DIR", path, len(path), unixSocketPathMax-1)
	}
	return nil
}

// startControlListener creates the control socket at path (0600 in a 0700
// directory) and serves one request per connection with handle. A socket at
// path that still answers belongs to a live frontend of the same key and is
// refused, not taken over; a stale one is replaced. Only callers running as
// this user are answered.
func startControlListener(path string, handle func(controlMessage) controlReply) (*controlListener, error) {
	if err := checkUnixSocketPath(path); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("control socket %s is live: another clown frontend serves this key", path)
	}
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// Close unlinks the path itself, and only while it is still this socket.
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	bound, err := os.Stat(path)
	if err != nil {
		ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	l := &controlListener{ln: ln, path: path, bound: bound}
	go func() {
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			go serveControlConn(c, handle)
		}
	}()
	return l, nil
}

func serveControlConn(c *net.UnixConn, handle func(controlMessage) controlReply) {
	defer c.Close()
	if !peerIsSameUser(c) {
		return
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(c, 64<<10)).ReadBytes('\n')
	var reply controlReply
	var m controlMessage
	switch {
	case err != nil:
		reply.Error = "reading the control request: " + err.Error()
	case json.Unmarshal(line, &m) != nil:
		reply.Error = "the control request is not JSON"
	default:
		reply = handle(m)
	}
	out, _ := json.Marshal(reply)
	_, _ = c.Write(append(out, '\n'))
}

// peerIsSameUser reports whether the connected peer runs as this process's
// user (SO_PEERCRED). The 0600 socket already keeps others out; this is the
// second check.
func peerIsSameUser(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return false
	}
	return int(cred.Uid) == os.Getuid()
}

// Close stops listening and removes the socket, unless the path now holds
// another listener's socket (a successor that replaced it).
func (l *controlListener) Close() {
	l.once.Do(func() {
		l.ln.Close()
		if fi, err := os.Stat(l.path); err == nil && os.SameFile(fi, l.bound) {
			_ = os.Remove(l.path)
		}
	})
}

// armRestartControl binds the control socket for a launch that runs its
// provider as a child (so not --naked, not codex, which exec-replace clown,
// and never --print-launch-plan), whether or not the idle trigger is armed:
// an unarmed frontend still answers, so `clown restart` can say why it will
// not restart. A failure to bind only means `clown restart` finds no frontend.
//
// trig is the idle runner's triggers (nil when the idle trigger is not armed,
// which unarmedReason then refuses anyway).
func armRestartControl(flags parsedFlags, unarmedReason string, q *restartQueue, trig *restartTriggers) (closeFn func()) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if flags.naked || flags.printLaunchPlan || flags.provider == "codex" || flags.identity.Key == "" || !filepath.IsAbs(runtime) {
		return func() {}
	}
	l, err := startControlListener(controlSocketPath(runtime, flags.identity.Key), restartControlHandlerTriggers(flags.provider, unarmedReason, q, trig))
	if err != nil {
		fmt.Fprintf(os.Stderr, "clown: restart control socket not bound: %v\n", err)
		return func() {}
	}
	return l.Close
}

// restartUnarmedReason is why this launch's idle trigger is not armed ("" when
// it is): selfCloseDecision's gate, or a listener that failed to start.
func restartUnarmedReason(flags parsedFlags, idleArmed bool) string {
	// The relaunch resumes the principal's conversation: a launch whose
	// claude conversation id is something else (-p, --continue, a non-UUID
	// operator key) could not be resumed under the same principal.
	if flags.provider == "claude" && flags.resumeHintID != flags.identity.Key {
		return "this launch's claude conversation id is not its principal, so a relaunch could not resume it"
	}
	if idleArmed {
		return ""
	}
	if _, reason := selfCloseDecision(flags); reason != "" {
		return reason
	}
	return "the idle listener did not start (see close-self-run.log)"
}

// validSessionKey reports whether key is one path segment, as the socket name
// needs.
func validSessionKey(key string) bool {
	return key != "" && key != "." && key != ".." && !strings.ContainsAny(key, "/\x00")
}

// restartVerbUsage is `clown restart`'s usage line.
const restartVerbUsage = "usage: clown restart --session KEY [--when idle|now] [--reason TEXT] [--posture unattended|attended]"

// runRestartVerb is `clown restart --session K [--when idle|now] [--reason R]
// [--posture P]`. It exits 0 once the frontend has accepted the request, and
// non-zero, with the reason on stderr, when there is no live frontend for K or
// the frontend refuses (spinclass clears the request and shows that output).
// For `now`, accepted means the frontend has started asking spinclass; a
// refusal by the preflight after that is spinclass's to report. --posture is
// the hand-off's argument (clown#342): the frontend gates it at request time
// and the preflight's verdict decides what the relaunch takes.
func runRestartVerb(args []string, stdout, stderr io.Writer) int {
	var session, reason, posture string
	when := "idle"
	sessionSet := false
	for len(args) > 0 {
		flag := args[0]
		if flag != "--session" && flag != "--when" && flag != "--reason" && flag != "--posture" {
			fmt.Fprintf(stderr, "clown restart: unknown argument %q\n%s\n", flag, restartVerbUsage)
			return 2
		}
		if len(args) < 2 {
			fmt.Fprintf(stderr, "clown restart: %s needs a value\n", flag)
			return 2
		}
		switch flag {
		case "--session":
			session, sessionSet = args[1], true
		case "--when":
			when = args[1]
		case "--reason":
			reason = args[1]
		case "--posture":
			posture = args[1]
		}
		args = args[2:]
	}
	if !sessionSet {
		fmt.Fprintf(stderr, "clown restart: --session is required\n%s\n", restartVerbUsage)
		return 2
	}
	if !validSessionKey(session) {
		fmt.Fprintf(stderr, "clown restart: --session %q is not a session key\n", session)
		return 2
	}
	if when != "idle" && when != "now" {
		fmt.Fprintf(stderr, "clown restart: --when %q is not supported; use idle or now\n", when)
		return 2
	}
	if !validPosture(posture) {
		fmt.Fprintf(stderr, "clown restart: --posture %q is not supported; use unattended or attended\n", posture)
		return 2
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		fmt.Fprintf(stderr, "no live clown frontend for %s\n", session)
		return 1
	}
	path := controlSocketPath(runtime, session)
	if err := checkUnixSocketPath(path); err != nil {
		fmt.Fprintf(stderr, "clown restart: %v\n", err)
		return 1
	}
	reply, err := sendControl(path, controlMessage{Op: "restart", When: when, Reason: reason, Posture: posture})
	if errors.Is(err, errNoFrontend) {
		fmt.Fprintf(stderr, "no live clown frontend for %s\n", session)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "clown restart: %v\n", err)
		return 1
	}
	if !reply.OK {
		fmt.Fprintln(stderr, reply.Error)
		return 1
	}
	if posture != postureKeep {
		fmt.Fprintf(stdout, "restart queued for %s (when %s, posture %s)\n", session, when, posture)
		return 0
	}
	fmt.Fprintf(stdout, "restart queued for %s (when %s)\n", session, when)
	return 0
}

var errNoFrontend = errors.New("no live frontend")

// sendControl writes m to the control socket at path and reads the reply. A
// socket that is absent or does not accept is errNoFrontend.
func sendControl(path string, m controlMessage) (controlReply, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return controlReply{}, errNoFrontend
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	out, err := json.Marshal(m)
	if err != nil {
		return controlReply{}, err
	}
	if _, err := c.Write(append(out, '\n')); err != nil {
		return controlReply{}, fmt.Errorf("writing to the frontend: %w", err)
	}
	line, err := bufio.NewReader(io.LimitReader(c, 64<<10)).ReadBytes('\n')
	if err != nil {
		return controlReply{}, fmt.Errorf("reading the frontend's reply: %w", err)
	}
	var reply controlReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return controlReply{}, fmt.Errorf("the frontend's reply is not JSON: %w", err)
	}
	return reply, nil
}

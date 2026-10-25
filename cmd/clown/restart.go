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

// restartRequest is one queued restart.
type restartRequest struct {
	When   string `json:"when"`
	Reason string `json:"reason"`
}

// controlMessage is the one JSON line `clown restart` writes.
type controlMessage struct {
	Op     string `json:"op"`
	When   string `json:"when"`
	Reason string `json:"reason"`
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
}

func (q *restartQueue) put(r restartRequest) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = &r
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

// restartControlHandler answers control messages for a frontend running
// provider. unarmedReason is selfCloseDecision's reason ("" when the idle
// trigger is armed): without the trigger nothing would ever act on a queued
// restart, so it is refused rather than queued.
func restartControlHandler(provider, unarmedReason string, q *restartQueue) func(controlMessage) controlReply {
	return func(m controlMessage) controlReply {
		if m.Op != "restart" {
			return controlReply{Error: fmt.Sprintf("unknown control op %q", m.Op)}
		}
		if m.When != "idle" {
			return controlReply{Error: fmt.Sprintf("restart --when %q is not supported; only idle", m.When)}
		}
		if unarmedReason != "" {
			if provider != "claude" {
				return controlReply{Error: fmt.Sprintf("restart needs an idle trigger; %s has none", provider)}
			}
			return controlReply{Error: fmt.Sprintf("restart needs an idle trigger; %s's is not armed (%s)", provider, unarmedReason)}
		}
		q.put(restartRequest{When: m.When, Reason: sanitizeRestartReason(m.Reason)})
		return controlReply{OK: true}
	}
}

// restartReasonMaxRunes caps a restart reason.
const restartReasonMaxRunes = 300

// sanitizeRestartReason reduces a restart reason to one line of printable
// text: control characters (C0, C1, newlines) become spaces, other
// non-printing runes (bidi controls, zero-width characters) are dropped,
// runs of space collapse, and it is capped at restartReasonMaxRunes.
func sanitizeRestartReason(s string) string {
	clean := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r) || unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, s)), " ")
	if r := []rune(clean); len(r) > restartReasonMaxRunes {
		clean = string(r[:restartReasonMaxRunes]) + "..."
	}
	return clean
}

// controlListener serves the control socket.
type controlListener struct {
	ln   *net.UnixListener
	path string
	once sync.Once
}

// startControlListener creates the control socket at path (0600 in a 0700
// directory) and serves one request per connection with handle. A socket at
// path that still answers belongs to a live frontend of the same key and is
// refused, not taken over; a stale one is replaced. Only callers running as
// this user are answered.
func startControlListener(path string, handle func(controlMessage) controlReply) (*controlListener, error) {
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
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	l := &controlListener{ln: ln, path: path}
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

// Close stops listening and removes the socket.
func (l *controlListener) Close() {
	l.once.Do(func() {
		l.ln.Close()
		_ = os.Remove(l.path)
	})
}

// armRestartControl binds the control socket for a launch that runs its
// provider as a child (so not --naked, not codex, which exec-replace clown,
// and never --print-launch-plan), whether or not the idle trigger is armed:
// an unarmed frontend still answers, so `clown restart` can say why it will
// not restart. A failure to bind only means `clown restart` finds no frontend.
func armRestartControl(flags parsedFlags, unarmedReason string, q *restartQueue) (closeFn func()) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if flags.naked || flags.printLaunchPlan || flags.provider == "codex" || flags.identity.Key == "" || !filepath.IsAbs(runtime) {
		return func() {}
	}
	l, err := startControlListener(controlSocketPath(runtime, flags.identity.Key), restartControlHandler(flags.provider, unarmedReason, q))
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

// runRestartVerb is `clown restart --session K [--when idle] [--reason R]`.
// It exits 0 once the frontend has queued the request, and non-zero, with the
// reason on stderr, when there is no live frontend for K or the frontend
// refuses (spinclass clears the request and shows that output).
func runRestartVerb(args []string, stdout, stderr io.Writer) int {
	var session, reason string
	when := "idle"
	sessionSet := false
	for len(args) > 0 {
		flag := args[0]
		if flag != "--session" && flag != "--when" && flag != "--reason" {
			fmt.Fprintf(stderr, "clown restart: unknown argument %q\nusage: clown restart --session KEY [--when idle] [--reason TEXT]\n", flag)
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
		}
		args = args[2:]
	}
	if !sessionSet {
		fmt.Fprintln(stderr, "clown restart: --session is required\nusage: clown restart --session KEY [--when idle] [--reason TEXT]")
		return 2
	}
	if !validSessionKey(session) {
		fmt.Fprintf(stderr, "clown restart: --session %q is not a session key\n", session)
		return 2
	}
	if when != "idle" {
		fmt.Fprintf(stderr, "clown restart: --when %q is not supported; only idle\n", when)
		return 2
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		fmt.Fprintf(stderr, "no live clown frontend for %s\n", session)
		return 1
	}
	reply, err := sendControl(controlSocketPath(runtime, session), controlMessage{Op: "restart", When: when, Reason: reason})
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

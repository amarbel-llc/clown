package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/buildcfg"
)

// The relaunch resumes the same conversation: any --session-id/--resume the
// launch carried is replaced by --resume <principal>, and the resolved flags
// (provider, profile) ride along so the profile gate sees the same request.
func TestRestartArgvResumesThePrincipal(t *testing.T) {
	f := parsedFlags{provider: "claude", profile: "worker", forwarded: []string{"--session-id", restartTestPrincipal, "--model", "opus"}}
	f.identity.Key = restartTestPrincipal
	got := restartArgv(f, "")
	want := []string{"--provider", "claude", "--profile", "worker", "--", "--model", "opus", "--resume", restartTestPrincipal}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}

	for _, fwd := range [][]string{
		{"--resume", restartTestPrincipal},
		{"--resume=" + restartTestPrincipal},
		{"-r", restartTestPrincipal},
		{"--session-id=" + restartTestPrincipal},
	} {
		f.forwarded = fwd
		got := restartArgv(f, "")
		if n := strings.Count(strings.Join(got, " "), restartTestPrincipal); n != 1 || got[len(got)-2] != "--resume" {
			t.Errorf("forwarded %q: argv = %q, want exactly one trailing --resume", fwd, got)
		}
	}

	// Only the resume occurrences claude's parser reads are dropped; operands
	// after `--` stay, and the new --resume goes before them (clown#318).
	f.forwarded = []string{"-rabc", "-r=x", "--model", "m", "--", "-rf", "--resume", "y"}
	got = restartArgv(f, "")
	want = []string{"--provider", "claude", "--profile", "worker", "--", "--model", "m", "--resume", restartTestPrincipal, "--", "-rf", "--resume", "y"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}

	// The inner clown of an [attach] wrap keeps its id, so it does not wrap again.
	f.forwarded = nil
	if got := restartArgv(f, restartTestPrincipal); got[0] != attachIDFlag || got[1] != restartTestPrincipal {
		t.Fatalf("attached argv = %q, want %s first", got, attachIDFlag)
	}
}

// The relaunch env carries the principal and the notice, and drops the
// launcher's one-shot profile approval and any other session id.
func TestRestartEnv(t *testing.T) {
	env := restartEnv([]string{
		"PATH=/bin", "CLOWN_SESSION_ID=old", "CLAUDE_SESSION_ID=x",
		"CLOWN_PROFILE=p", "CLOWN_PROFILE_DIGEST=d", "CLOWN_PROFILE_SESSION=s",
		"CLOWN_RESTARTED_REASON=stale", "TROUPE_XMPP_USER=u",
	}, &relaunchPlan{principal: restartTestPrincipal, reason: "new tools"})
	for _, want := range []string{"PATH=/bin", "CLOWN_SESSION_ID=" + restartTestPrincipal, "CLOWN_RESTARTED_REASON=new tools", "TROUPE_XMPP_USER=u"} {
		if !slices.Contains(env, want) {
			t.Errorf("env missing %q: %q", want, env)
		}
	}
	for _, gone := range []string{"CLOWN_SESSION_ID=old", "CLAUDE_SESSION_ID=x", "CLOWN_PROFILE=p", "CLOWN_PROFILE_DIGEST=d", "CLOWN_PROFILE_SESSION=s", "CLOWN_RESTARTED_REASON=stale"} {
		if slices.Contains(env, gone) {
			t.Errorf("env still has %q", gone)
		}
	}
}

func TestRestartedNoticeIsOneBoundedLine(t *testing.T) {
	if got := restartedNotice(""); got != "" {
		t.Fatalf("no restart: %q", got)
	}
	n := restartedNotice("pick up\nnew\x1btools")
	if !strings.Contains(n, "restarted: pick up new tools") {
		t.Fatalf("notice = %q", n)
	}
	// The replayed-brief sentence (a relaunch re-sends the spawn brief as a new
	// message; one worker redid its whole task on seeing it): a replay is named
	// as such, finishing is allowed to be the answer, and redoing is not.
	for _, want := range []string{"original brief", "replay caused by this restart", "if you finished the task it describes, say so and stop", "do not redo it"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice %q lacks %q", n, want)
		}
	}
	if long := restartedNotice(strings.Repeat("x", 5000)); len(long) > 1000 {
		t.Fatalf("notice not bounded: %d bytes", len(long))
	}
}

func listenTestServeSocket(t *testing.T, runtime string) net.Listener {
	t.Helper()
	dir := filepath.Join(runtime, "clown", "serve", restartTestPrincipal)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "spinclass.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

type execCall struct {
	path       string
	argv, env  []string
	socketLive bool
	at         time.Time
}

func testRelauncher(t *testing.T, runtime string, calls *[]execCall) relauncher {
	t.Helper()
	return relauncher{
		runtimeDir: runtime,
		environ:    []string{"PATH=/bin"},
		lookPath:   func(string) (string, error) { return "/new/bin/clown", nil },
		exec: func(path string, argv, env []string) error {
			c, err := net.DialTimeout("unix", filepath.Join(runtime, "clown", "serve", restartTestPrincipal, "spinclass.sock"), 100*time.Millisecond)
			if err == nil {
				c.Close()
			}
			*calls = append(*calls, execCall{path: path, argv: argv, env: env, socketLive: err == nil, at: time.Now()})
			return errors.New("exec stubbed")
		},
		serveWait: 3 * time.Second,
	}
}

// fdda002 refuses a live serve socket, so the relaunch waits for the old
// bridge's socket to stop answering before it execs the new frontend.
func TestRelaunchWaitsForTheOldServeSocket(t *testing.T) {
	runtime := shortSocketDir(t)
	ln := listenTestServeSocket(t, runtime)
	closedAt := make(chan time.Time, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		closedAt <- time.Now()
		ln.Close()
	}()
	var calls []execCall
	var stderr bytes.Buffer
	r := testRelauncher(t, runtime, &calls)
	r.stderr = &stderr
	r.relaunch(&relaunchPlan{principal: restartTestPrincipal, reason: "r", argv: []string{"--provider", "claude"}})
	if len(calls) != 1 {
		t.Fatalf("exec calls = %d (stderr %q), want one", len(calls), stderr.String())
	}
	if calls[0].socketLive || calls[0].at.Before(<-closedAt) {
		t.Fatal("exec ran while the old serve socket was still live")
	}
	if calls[0].path != "/new/bin/clown" || !slices.Equal(calls[0].argv, []string{"/new/bin/clown", "--provider", "claude"}) {
		t.Fatalf("exec = %s %q, want the clown on PATH with the plan's argv", calls[0].path, calls[0].argv)
	}
}

// A serve socket that never goes away fails the relaunch loudly, without exec.
func TestRelaunchRefusesWhileTheServeSocketStaysLive(t *testing.T) {
	runtime := shortSocketDir(t)
	listenTestServeSocket(t, runtime)
	var calls []execCall
	var stderr bytes.Buffer
	r := testRelauncher(t, runtime, &calls)
	r.stderr = &stderr
	r.serveWait = 300 * time.Millisecond
	code := r.relaunch(&relaunchPlan{principal: restartTestPrincipal, reason: "r"})
	if code == 0 || len(calls) != 0 {
		t.Fatalf("code %d, exec calls %d; want a failure and no exec", code, len(calls))
	}
	if !strings.Contains(stderr.String(), "still live") || !strings.Contains(stderr.String(), "not relaunching") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// A restart stops the provider only while one runs, and records the plan for
// main before it does.
func TestFrontendRestarterStopsTheProviderOnce(t *testing.T) {
	pendingRelaunch.Store(nil)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	stops := 0
	graceDone := make(chan struct{}, 1)
	t.Cleanup(func() { <-graceDone })
	fr := frontendRestarter{
		principal: restartTestPrincipal, argv: []string{"--provider", "claude"}, stop: func() {
			stops++
			if pendingRelaunch.Load() == nil {
				t.Error("the plan must be recorded before the provider is stopped")
			}
		}, logPath: filepath.Join(t.TempDir(), "l.log"), stopGrace: time.Millisecond, kill: func() error { return nil },
		graceChecked: func(bool) { graceDone <- struct{}{} },
	}

	providerRunning.Store(false)
	fr.restart(restartRequest{When: "idle", Reason: "r"}, preflightVerdict{Verdict: "restart"})
	if stops != 0 || pendingRelaunch.Load() != nil {
		t.Fatal("no provider running: nothing to stop")
	}
	providerRunning.Store(true)
	fr.restart(restartRequest{When: "idle", Reason: "new tools"}, preflightVerdict{Verdict: "restart", Reason: "other"})
	if stops != 1 {
		t.Fatalf("stops = %d", stops)
	}
	if p := pendingRelaunch.Load(); p == nil || p.reason != "other" || p.principal != restartTestPrincipal {
		t.Fatalf("plan = %+v", p)
	}
}

// troupe: the relaunched session keeps its principal, so its XMPP account must
// survive. From the idle poke to the exec, a restart never runs `troupe
// mint-revoke` (nor any troupe verb): only a real close revokes.
func TestRestartNeverRevokesTheIdentity(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })

	dir := t.TempDir()
	troupeLog := filepath.Join(dir, "troupe.calls")
	troupe := filepath.Join(dir, "troupe")
	if err := os.WriteFile(troupe, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+troupeLog+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := buildcfg.TroupePath
	buildcfg.TroupePath = troupe
	t.Cleanup(func() { buildcfg.TroupePath = old })
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	bin, _ := fakeIdleSpinclass(t, `{"verdict":"restart","reason":"r","when":"idle"}`+"\n", "0")
	q := &restartQueue{}
	q.put(restartRequest{When: "idle", Reason: "r"})
	stopped := 0
	logPath := filepath.Join(t.TempDir(), "l.log")
	graceDone := make(chan struct{}, 1)
	t.Cleanup(func() { <-graceDone })
	fr := frontendRestarter{
		principal: restartTestPrincipal, argv: []string{"--provider", "claude"}, stop: func() { stopped++ }, logPath: logPath,
		stopGrace: time.Millisecond, kill: func() error { return nil },
		graceChecked: func(bool) { graceDone <- struct{}{} },
	}
	idle := &idleRunner{
		closer:    &selfCloseRunner{bin: bin, principal: restartTestPrincipal, logPath: logPath},
		preflight: &restartPreflight{bin: bin, principal: restartTestPrincipal, logPath: logPath},
		queue:     q,
		restart:   fr.restart,
		logPath:   logPath,
	}
	idle.onIdle()
	if stopped != 1 {
		t.Fatalf("stopped = %d, want the provider stopped once", stopped)
	}

	runtime := shortSocketDir(t)
	var calls []execCall
	r := testRelauncher(t, runtime, &calls)
	r.stderr = &bytes.Buffer{}
	r.relaunch(pendingRelaunch.Load())
	if len(calls) != 1 {
		t.Fatalf("exec calls = %d", len(calls))
	}
	if !slices.Contains(calls[0].env, "CLOWN_SESSION_ID="+restartTestPrincipal) {
		t.Fatalf("relaunch env lacks the principal: %q", calls[0].env)
	}
	if b, err := os.ReadFile(troupeLog); err == nil {
		t.Fatalf("a restart ran troupe: %q", b)
	}
}

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// S1: spinclass removes the worktree last on close, after stopping the unit,
// so the check runs immediately before the exec, after the serve-socket wait:
// a worktree removed while the frontend waited is not relaunched.
func TestRelaunchRechecksTheWorktreeRightBeforeExec(t *testing.T) {
	runtime := shortSocketDir(t)
	ln := listenTestServeSocket(t, runtime)
	worktree := t.TempDir()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.RemoveAll(worktree)
		ln.Close()
	}()
	var calls []execCall
	r := testRelauncher(t, runtime, &calls)
	r.stderr = &bytes.Buffer{}
	r.logPath = filepath.Join(t.TempDir(), "close-self-run.log")
	code := r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: worktree})
	if len(calls) != 0 || code != 0 {
		t.Fatalf("exec calls %d, code %d; a session closed during the wait must not relaunch", len(calls), code)
	}
	b, _ := os.ReadFile(r.logPath)
	if !strings.Contains(string(b), "not relaunching") {
		t.Fatalf("log = %q, want the skip and its reason", b)
	}
}

// S3/S5: every relaunch outcome lands in close-self-run.log with its reason.
func TestRelaunchLogsEveryOutcome(t *testing.T) {
	for name, c := range map[string]struct {
		lookErr, execErr error
		live             bool
		want             string
	}{
		"serve socket live": {live: true, want: "still live"},
		"no clown on PATH":  {lookErr: errors.New("not found"), want: "no clown on PATH"},
		"exec failed":       {execErr: errors.New("exec format error"), want: "exec format error"},
		"relaunching":       {want: "relaunching"},
	} {
		t.Run(name, func(t *testing.T) {
			runtime := shortSocketDir(t)
			if c.live {
				listenTestServeSocket(t, runtime)
			}
			var calls []execCall
			r := testRelauncher(t, runtime, &calls)
			r.stderr = &bytes.Buffer{}
			r.serveWait = 200 * time.Millisecond
			r.logPath = filepath.Join(t.TempDir(), "close-self-run.log")
			if c.lookErr != nil {
				r.lookPath = func(string) (string, error) { return "", c.lookErr }
			}
			if c.execErr != nil {
				r.exec = func(string, []string, []string) error { return c.execErr }
			}
			r.relaunch(&relaunchPlan{principal: restartTestPrincipal, worktree: t.TempDir()})
			b, _ := os.ReadFile(r.logPath)
			if !strings.Contains(string(b), c.want) {
				t.Fatalf("log = %q, want %q", b, c.want)
			}
		})
	}
}

// S9: the restarter's worktree reaches the plan main checks.
func TestRestarterRecordsTheWorktreeInThePlan(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	checked := make(chan bool, 1)
	fr := frontendRestarter{
		principal: restartTestPrincipal, stop: func() {}, worktree: "/w/t",
		logPath: filepath.Join(t.TempDir(), "l.log"), stopGrace: time.Millisecond,
		kill: func() error { return nil }, graceChecked: func(k bool) { checked <- k },
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	<-checked
	if p := pendingRelaunch.Load(); p == nil || p.worktree != "/w/t" {
		t.Fatalf("plan = %+v", p)
	}
}

// S3: a failed SIGKILL after the grace period is logged, not dropped.
func TestAFailedKillIsLogged(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	logPath := filepath.Join(t.TempDir(), "l.log")
	checked := make(chan bool, 1)
	fr := frontendRestarter{
		principal: restartTestPrincipal, stop: func() {}, logPath: logPath, stopGrace: time.Millisecond,
		kill: func() error { return errors.New("systemctl timed out") }, graceChecked: func(k bool) { checked <- k },
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	<-checked
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "systemctl timed out") {
		t.Fatalf("log = %q", b)
	}
}

// S8: the "cannot write" line goes to stderr once per process, not on every
// idle transition.
func TestCannotWriteLogWarnsOnce(t *testing.T) {
	var warned bytes.Buffer
	old := logWarnings
	logWarnings = &warned
	t.Cleanup(func() { logWarnings = old; logWarnOnce = newOnce() })
	logWarnOnce = newOnce()
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		logSelfClose(filepath.Join(bad, "under-a-file.log"), "x")
	}
	if n := strings.Count(warned.String(), "cannot write"); n != 1 {
		t.Fatalf("warnings = %d: %q", n, warned.String())
	}
}

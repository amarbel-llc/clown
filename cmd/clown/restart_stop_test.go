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

// M2: a provider that ignores the stop keeps running; the plan is dropped
// after the grace period, so a later /exit ends the session instead of
// relaunching it.
func TestRestartDropsThePlanWhenTheProviderDoesNotExit(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	logPath := filepath.Join(t.TempDir(), "l.log")
	fr := frontendRestarter{principal: restartTestPrincipal, stop: func() {}, stopGrace: 50 * time.Millisecond, logPath: logPath}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	if pendingRelaunch.Load() == nil {
		t.Fatal("the plan must be recorded before the stop")
	}
	waitFor(t, "the plan to be dropped", func() bool { return pendingRelaunch.Load() == nil })
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "did not exit") {
		t.Fatalf("log = %s", b)
	}
}

// A provider that does exit keeps its plan.
func TestRestartKeepsThePlanWhenTheProviderExits(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	fr := frontendRestarter{principal: restartTestPrincipal, stop: func() { providerRunning.Store(false) }, stopGrace: 50 * time.Millisecond, logPath: filepath.Join(t.TempDir(), "l.log")}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	time.Sleep(200 * time.Millisecond)
	if pendingRelaunch.Load() == nil {
		t.Fatal("an exited provider's plan must stay for main")
	}
}

// M3: a failed exec still tells the operator how to get the session back.
func TestRelaunchExecFailureNamesTheResumeCommand(t *testing.T) {
	var stderr bytes.Buffer
	r := relauncher{
		runtimeDir: shortSocketDir(t),
		lookPath:   func(string) (string, error) { return "/new/bin/clown", nil },
		exec:       func(string, []string, []string) error { return errors.New("exec format error") },
		stderr:     &stderr,
		serveWait:  time.Second,
	}
	if code := r.relaunch(&relaunchPlan{principal: restartTestPrincipal}); code == 0 {
		t.Fatal("a failed exec must exit non-zero")
	}
	if !strings.Contains(stderr.String(), "clown resume "+restartTestPrincipal) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

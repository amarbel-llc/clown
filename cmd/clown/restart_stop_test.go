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

// F2: a provider that ignores the stop is killed after the grace period and
// the plan is kept, so its exit still relaunches the session.
func TestRestartKillsAProviderThatIgnoresTheStop(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	logPath := filepath.Join(t.TempDir(), "l.log")
	killed := make(chan struct{}, 1)
	checked := make(chan bool, 1)
	fr := frontendRestarter{
		principal: restartTestPrincipal, stop: func() {}, stopGrace: 50 * time.Millisecond, logPath: logPath,
		kill:         func() error { killed <- struct{}{}; return nil },
		graceChecked: func(k bool) { checked <- k },
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	plan := pendingRelaunch.Load()
	if plan == nil {
		t.Fatal("the plan must be recorded before the stop")
	}
	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider was not killed after the grace period")
	}
	select {
	case k := <-checked:
		if !k || pendingRelaunch.Load() != plan {
			t.Fatal("the plan must survive the kill, so the exit relaunches")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the grace check never ran")
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "SIGKILL") {
		t.Fatalf("log = %s", b)
	}
}

// A provider that exits in time is not killed, and keeps its plan.
func TestRestartDoesNotKillAProviderThatExits(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	kills := 0
	checked := make(chan bool, 1)
	fr := frontendRestarter{
		principal: restartTestPrincipal, stop: func() { providerRunning.Store(false) }, stopGrace: 50 * time.Millisecond,
		logPath: filepath.Join(t.TempDir(), "l.log"),
		kill:    func() error { kills++; return nil }, graceChecked: func(k bool) { checked <- k },
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	select {
	case k := <-checked:
		if k || kills != 0 {
			t.Fatalf("an exited provider was killed (%v, %d)", k, kills)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the grace check never ran")
	}
	if pendingRelaunch.Load() == nil {
		t.Fatal("an exited provider's plan must stay for main")
	}
}

// F9: a provider that exited during the countdown is not "stopped", and no
// plan is stored for it.
func TestRestartRechecksTheProviderAfterTheCountdown(t *testing.T) {
	pendingRelaunch.Store(nil)
	providerRunning.Store(true)
	t.Cleanup(func() { pendingRelaunch.Store(nil); providerRunning.Store(false) })
	stops := 0
	fr := frontendRestarter{
		principal: restartTestPrincipal, stop: func() { stops++ }, logPath: filepath.Join(t.TempDir(), "l.log"),
		countdown: func(string, bool) (bool, string) { providerRunning.Store(false); return true, "elapsed" },
	}
	fr.restart(restartRequest{When: "idle"}, preflightVerdict{Verdict: "restart", Reason: "r"})
	if stops != 0 || pendingRelaunch.Load() != nil {
		t.Fatalf("stops %d, plan %v; the provider had already exited", stops, pendingRelaunch.Load())
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

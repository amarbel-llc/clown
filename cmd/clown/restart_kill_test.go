package main

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// R3/S9: killProvider kills only while the provider is recorded as running.
// The second half leaves the kill recorded and clears only providerRunning,
// so it fails if the running check is removed.
func TestKillProviderOnlyWhileRunning(t *testing.T) {
	t.Cleanup(clearProviderRunning)
	kills := 0
	setProviderRunning(func() error { kills++; return nil })
	if err := killProvider(); err != nil || kills != 1 {
		t.Fatalf("kills = %d (%v) while running", kills, err)
	}
	providerRunning.Store(false) // the kill stays recorded
	if err := killProvider(); err != nil || kills != 1 {
		t.Fatalf("killed (%d) after the provider exited", kills)
	}
}

// S2: a kill that hangs does not hold the lock: marking the provider exited
// still returns at once.
func TestAHangingKillDoesNotBlockProviderExit(t *testing.T) {
	t.Cleanup(clearProviderRunning)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	entered := make(chan struct{})
	setProviderRunning(func() error { close(entered); <-release; return nil })
	go func() { _ = killProvider() }()
	<-entered
	done := make(chan struct{})
	go func() { clearProviderRunning(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("clearProviderRunning waited for a hanging kill")
	}
}

// R3: the direct and pty kill goes through the frontend's own process handle,
// so a provider already reaped is never signalled by a reused pid.
func TestProcessKillAfterWaitSignalsNothing(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := killProcess(cmd.Process); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill after Wait = %v, want os.ErrProcessDone", err)
	}
}

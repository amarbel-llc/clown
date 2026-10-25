package main

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

// R3: killProvider kills only while the provider is recorded as running.
func TestKillProviderOnlyWhileRunning(t *testing.T) {
	t.Cleanup(clearProviderRunning)
	kills := 0
	setProviderRunning(func() { kills++ })
	killProvider()
	if kills != 1 {
		t.Fatalf("kills = %d while running", kills)
	}
	clearProviderRunning()
	killProvider()
	if kills != 1 {
		t.Fatal("killed after the provider exited")
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

package main

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	rm "github.com/amarbel-llc/clown/internal/ringmaster"
)

// TestLauncher_ReapsUnexpectedExit verifies that when a llama-server
// child is killed externally (e.g. SIGKILL from another process), the
// launcher removes the alias from the registry without waiting for an
// explicit Stop call.
func TestLauncher_ReapsUnexpectedExit(t *testing.T) {
	bin := buildFakeLlamaServer(t)
	reg := rm.NewRegistry()
	l := newLauncher(bin, reg, filepath.Join(t.TempDir(), "models"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	in, err := l.Start(ctx, rm.StartInstanceParams{
		Alias: "doomed",
		Model: "doomed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("doomed"); !ok {
		t.Fatal("instance should be in registry after Start")
	}

	// SIGKILL the process group out from under the launcher.
	if err := syscall.Kill(-in.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Wait for the reaper to remove it. Poll on the registry; the
	// reaper should pick this up within a few hundred ms (the
	// goroutine's cmd.Wait returns immediately on death).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Get("doomed"); !ok {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(`alias "doomed" still in registry after 2s`)
}

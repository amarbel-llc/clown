package daemon_test

import (
	"os"
	"testing"

	"github.com/amarbel-llc/clown/internal/daemon"
)

func TestWriteAndReadPID(t *testing.T) {
	path := t.TempDir() + "/test.pid"
	if err := daemon.WritePID(path, 12345); err != nil {
		t.Fatal(err)
	}
	pid, err := daemon.ReadPID(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 12345 {
		t.Fatalf("want 12345, got %d", pid)
	}
}

func TestReadPIDMissing(t *testing.T) {
	_, err := daemon.ReadPID(t.TempDir() + "/nope.pid")
	if !os.IsNotExist(err) {
		t.Fatalf("want os.IsNotExist, got %v", err)
	}
}

func TestRemovePID(t *testing.T) {
	path := t.TempDir() + "/test.pid"
	_ = daemon.WritePID(path, 1)
	if err := daemon.RemovePID(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pidfile should be gone")
	}
}

func TestIsRunning(t *testing.T) {
	// Our own PID is definitely running.
	if !daemon.IsRunning(os.Getpid()) {
		t.Fatal("own process should be running")
	}
	// PID 0 is never a user process.
	if daemon.IsRunning(0) {
		t.Fatal("pid 0 should not be running")
	}
}

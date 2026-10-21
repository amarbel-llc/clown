package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// jobServices maps a test to the socket of the in-process ringmaster host
// service started for it (RFC-0020 §7.3), so a test that needs the job store
// starts it once however many fixtures it registers.
var jobServices sync.Map

// startJobService runs ringmaster's host service in-process for t: the store
// under the XDG_STATE_HOME the test already set, the socket under a fresh short
// XDG_RUNTIME_DIR (a socket path is limited to ~108 bytes). It returns the
// service's socket path and is idempotent per test.
func startJobService(t *testing.T) string {
	t.Helper()
	if v, ok := jobServices.Load(t); ok {
		return v.(string)
	}
	rt, err := os.MkdirTemp("", "rm")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", rt)
	stop, err := jobwake.StartService(nil)
	if err != nil {
		os.RemoveAll(rt)
		t.Fatalf("starting the in-process ringmaster service: %v", err)
	}
	sock := filepath.Join(rt, "ringmaster", "service.sock")
	jobServices.Store(t, sock)
	t.Cleanup(func() {
		stop()
		os.RemoveAll(rt)
		jobServices.Delete(t)
	})
	return sock
}

// aliasJobService points XDG_RUNTIME_DIR at a fresh directory whose service
// socket is a symlink to sock. jobwake's shared client captures the caller's
// environment (group, description, clown name) when it is built, and rebuilds
// only when the socket path changes, so a test that registers several
// differently-described sessions needs a distinct path for each.
func aliasJobService(t *testing.T, sock string) {
	t.Helper()
	rt, err := os.MkdirTemp("", "rm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	if err := os.MkdirAll(filepath.Join(rt, "ringmaster"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sock, filepath.Join(rt, "ringmaster", "service.sock")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", rt)
}

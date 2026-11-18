package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A hangup (posh's pty going away, `sc close`'s SIGHUP) reaches a provider run
// as a plain child, so runProvider returns and the deferred plugin-host
// Shutdown stops the plugin servers; unhandled, the SIGHUP would kill clown
// outright and leave them running.
func TestHangupIsForwardedToThePlainProvider(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "read x")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	forwardProviderSignals(cmd, nil)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("the provider exited with %v, want death by SIGHUP", err)
		}
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGHUP {
			t.Fatalf("the provider ended with %v, want death by SIGHUP", exitErr)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the SIGHUP sent to clown did not reach the provider")
	}
}

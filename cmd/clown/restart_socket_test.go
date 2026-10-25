package main

import (
	"os"
	"strings"
	"testing"
)

// M5: closing a listener whose path was taken over by a successor leaves the
// successor's socket in place and answering.
func TestControlSocketCloseKeepsASuccessorsSocket(t *testing.T) {
	runtime := shortSocketDir(t)
	path := controlSocketPath(runtime, restartTestPrincipal)
	first, err := startControlListener(path, restartControlHandler("claude", "", &restartQueue{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	q := &restartQueue{}
	second, err := startControlListener(path, restartControlHandler("claude", "", q))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the successor's socket was removed: %v", err)
	}
	if reply, err := sendControl(path, controlMessage{Op: "restart", When: "idle", Reason: "r"}); err != nil || !reply.OK {
		t.Fatalf("the successor must still answer: %+v, %v", reply, err)
	}
	if _, ok := q.take(); !ok {
		t.Fatal("the successor did not queue")
	}
}

// L5: a socket path over the unix limit is a clear error on both ends, not
// "no live frontend".
func TestControlSocketPathTooLong(t *testing.T) {
	runtime := "/" + strings.Repeat("r", 100)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	code, _, stderr := runRestartVerbForTest(t, "--session", restartTestPrincipal)
	if code == 0 || strings.Contains(stderr, "no live clown frontend") || !strings.Contains(stderr, "too long") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	if _, err := startControlListener(controlSocketPath(runtime, restartTestPrincipal), restartControlHandler("claude", "", &restartQueue{})); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("listener err = %v", err)
	}
}

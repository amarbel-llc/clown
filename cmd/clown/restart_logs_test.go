package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/sessionstatus"
)

// R9: a FIFO at a log path does not block the (serial) caller.
func TestLogSelfCloseDoesNotBlockOnAFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "close-self-run.log")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { logSelfClose(fifo, "x"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logSelfClose blocked on a FIFO")
	}
}

// R10: the stderr cancel line carries no control characters.
func TestCancelLineHasNoControlCharacters(t *testing.T) {
	var notice bytes.Buffer
	fr := frontendRestarter{principal: restartTestPrincipal, notify: &notice, logPath: filepath.Join(t.TempDir(), "l.log")}
	fr.cancelled(sessionstatus.RefusalProfileGate, "bad\x1b[2J\x07 profile")
	body := strings.Trim(notice.String(), "\r\n")
	if strings.ContainsAny(body, "\x1b\x07\r\n") {
		t.Fatalf("cancel line = %q", notice.String())
	}
}

// R7: an idle socket path over the unix limit is one clear error.
func TestIdleSocketPathTooLong(t *testing.T) {
	long := "/" + strings.Repeat("r", 110) + "/k.sock"
	if _, err := startIdleListener(long, func() {}); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("err = %v", err)
	}
}

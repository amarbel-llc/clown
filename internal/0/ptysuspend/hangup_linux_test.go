//go:build linux

package ptysuspend

import (
	"os"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/clown/internal/0/testlane"
)

// A hangup of the outer terminal (posh's pty going away, `sc close`'s SIGHUP)
// ends the child and returns from Run, so clown unwinds and stops its plugin
// servers; unhandled, the SIGHUP would kill this process outright and leave
// them running.
func TestSIGHUPIsForwardedToTheChild(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		testlane.SkipOrFail(t, "no /dev/ptmx on this host")
	}
	outerMaster, outer, err := openInnerPTY()
	if err != nil {
		t.Fatalf("openInnerPTY: %v", err)
	}
	defer outerMaster.Close()
	defer outer.Close()

	done := make(chan int, 1)
	go func() {
		code, _ := Run([]string{"/bin/sh", "-c", "read x"}, outer, Options{
			NoEscape: true,
			OnStart:  func(string, *os.Process) { _ = syscall.Kill(os.Getpid(), syscall.SIGHUP) },
		})
		done <- code
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the child did not get the SIGHUP sent to this process")
	}
}

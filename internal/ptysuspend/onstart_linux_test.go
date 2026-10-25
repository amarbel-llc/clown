//go:build linux

package ptysuspend

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// OnStart is called only once a process-level SIGTERM is forwarded to the
// child: clown's restart (spinclass#419) stops the provider by signalling
// itself from the moment OnStart has run. A SIGTERM sent from OnStart must
// end the child, not this process.
func TestOnStartRunsAfterSIGTERMIsForwarded(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx on this host")
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
			OnStart:  func(string, int) { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) },
		})
		done <- code
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the child did not get the SIGTERM sent from OnStart")
	}
}

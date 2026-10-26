package main

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// startFake starts argv as the shim's provider would be, with a SIGCHLD
// channel for superviseProvider.
func startFake(t *testing.T, argv ...string) (*os.Process, chan os.Signal) {
	t.Helper()
	path, err := exec.LookPath(argv[0])
	if err != nil {
		t.Skipf("%s: %v", argv[0], err)
	}
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGCHLD)
	t.Cleanup(func() { signal.Stop(sigs) })
	proc, err := os.StartProcess(path, argv, &os.ProcAttr{Files: []*os.File{nil, os.Stdout, os.Stderr}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Kill() })
	return proc, sigs
}

// supervise runs superviseProvider with signals sent to the fake provider
// alone (in the unit they go to every process in its pid namespace).
func supervise(proc *os.Process, sigs chan os.Signal, gone <-chan struct{}, grace time.Duration) (int, time.Duration) {
	start := time.Now()
	code := superviseProvider(proc, sigs, false, gone, grace, func(s syscall.Signal) { _ = proc.Signal(s) })
	return code, time.Since(start)
}

// The frontend's death (EOF on the dead-man socket) stops the provider with
// SIGTERM, within the grace (clown#319).
func TestFrontendDeathTerminatesTheProvider(t *testing.T) {
	proc, sigs := startFake(t, "sleep", "30")
	gone := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() { close(gone) })
	code, took := supervise(proc, sigs, gone, 5*time.Second)
	if code != 128+int(syscall.SIGTERM) || took > 3*time.Second {
		t.Fatalf("code %d after %v; want SIGTERM's 143 well within the grace", code, took)
	}
}

// A provider that ignores SIGTERM is killed when the grace runs out.
func TestFrontendDeathKillsAProviderThatIgnoresSIGTERM(t *testing.T) {
	proc, sigs := startFake(t, "sh", "-c", "trap '' TERM; sleep 5 & wait; sleep 5")
	gone := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() { close(gone) })
	code, took := supervise(proc, sigs, gone, 700*time.Millisecond)
	if code != 128+int(syscall.SIGKILL) || took > 3*time.Second {
		t.Fatalf("code %d after %v; want SIGKILL's 137 just after the grace", code, took)
	}
}

// A provider that cannot be reaped even after SIGKILL (uninterruptible sleep)
// does not keep the unit: the shim returns a bounded time after the SIGKILL,
// and its exit as init tears the namespace down.
func TestFrontendDeathEndsTheShimEvenIfTheProviderIsNeverReaped(t *testing.T) {
	proc, sigs := startFake(t, "sleep", "30")
	origReap, origWait := reapExited, deadManKillWait
	reapExited = func() (int, syscall.WaitStatus, bool) { return 0, 0, false }
	deadManKillWait = 300 * time.Millisecond
	t.Cleanup(func() { reapExited, deadManKillWait = origReap, origWait })
	var sent []syscall.Signal
	gone := make(chan struct{})
	close(gone)
	start := time.Now()
	code := superviseProvider(proc, sigs, false, gone, 200*time.Millisecond, func(s syscall.Signal) { sent = append(sent, s) })
	took := time.Since(start)
	if code != 128+int(syscall.SIGKILL) || took > 3*time.Second {
		t.Fatalf("code %d after %v; want 137 shortly after grace + kill wait", code, took)
	}
	if len(sent) < 2 || sent[0] != syscall.SIGTERM || sent[1] != syscall.SIGKILL {
		t.Fatalf("signals %v; want SIGTERM then SIGKILL", sent)
	}
}

// The shim refuses --deadman when it is not the unit's init: it could not
// stop the unit.
func TestAgentExecRefusesTheDeadManOutsideInit(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("running as pid 1")
	}
	if code := runAgentExec([]string{"--deadman", "/nonexistent.sock", "--", "true"}); code != 126 {
		t.Fatalf("runAgentExec = %d; want the refusal 126", code)
	}
}

// While the frontend lives nothing changes: the provider runs to its own end.
func TestALivingFrontendLeavesTheProviderAlone(t *testing.T) {
	proc, sigs := startFake(t, "sleep", "1")
	code, took := supervise(proc, sigs, make(chan struct{}), 100*time.Millisecond)
	if code != 0 || took < 900*time.Millisecond {
		t.Fatalf("code %d after %v; want the provider's own exit 0 after ~1s", code, took)
	}
	// And with no dead-man at all (nil channel), likewise.
	proc, sigs = startFake(t, "sleep", "0.3")
	if code, _ := supervise(proc, sigs, nil, 100*time.Millisecond); code != 0 {
		t.Fatalf("no dead-man: code %d", code)
	}
}

//go:build linux

package pluginhost

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const (
	pdeathsigRoleEnv     = "CLOWN_PLUGINHOST_PDEATHSIG_ROLE"
	prSetChildSubreaper  = 36
	pdeathsigWaitTimeout = 10 * time.Second
)

// TestPdeathsigHelper plays clown for TestManagedServerDiesWithItsHost: it
// starts a child the way Start does, prints the child's pid and waits to be
// SIGKILLed. Run alone it skips.
func TestPdeathsigHelper(t *testing.T) {
	switch os.Getenv(pdeathsigRoleEnv) {
	case "host":
		cmd := exec.Command(os.Args[0], "-test.run=^TestPdeathsigHelper$")
		cmd.Env = append(os.Environ(), pdeathsigRoleEnv+"=server")
		cmd.SysProcAttr = serverProcAttr()
		if _, err := startPinned(cmd); err != nil {
			fmt.Println("start:", err)
			os.Exit(2)
		}
		fmt.Println(cmd.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(3)
	case "server":
		time.Sleep(time.Minute)
		os.Exit(4)
	default:
		t.Skip("helper process for TestManagedServerDiesWithItsHost")
	}
}

// A managed server dies when clown is killed without unwinding (SIGKILL, an
// OOM kill, a hangup clown does not survive): the parent-death signal is a
// SIGTERM. The helper plays clown; this test becomes a child subreaper so the
// orphaned server is reparented here and its death can be read. It proves the
// signal is set and fires on the host's death; it cannot prove the start
// thread outlives other threads of a long-running clown (startPinned's lock).
func TestManagedServerDiesWithItsHost(t *testing.T) {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		t.Fatalf("PR_SET_CHILD_SUBREAPER: %v", errno)
	}
	defer syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 0, 0) //nolint:errcheck

	host := exec.Command(os.Args[0], "-test.run=^TestPdeathsigHelper$")
	host.Env = append(os.Environ(), pdeathsigRoleEnv+"=host")
	out, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	pid := 0
	sc := bufio.NewScanner(out)
	for pid == 0 && sc.Scan() {
		if n, err := strconv.Atoi(sc.Text()); err == nil {
			pid = n
		} else {
			t.Logf("helper: %s", sc.Text())
		}
	}
	if pid == 0 {
		_ = host.Process.Kill()
		_ = host.Wait()
		t.Fatal("the helper printed no server pid")
	}

	_ = host.Process.Kill()
	_ = host.Wait()

	var ws syscall.WaitStatus
	deadline := time.Now().Add(pdeathsigWaitTimeout)
	for {
		got, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if got == pid {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			_, _ = syscall.Wait4(pid, &ws, 0, nil)
			t.Fatalf("the server outlived its killed host by %s (last wait4 error: %v)", pdeathsigWaitTimeout, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("the server ended with %v, want death by SIGTERM", ws)
	}
}

//go:build linux

package pluginhost

import (
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

// serverProcAttr puts a managed server in its own process group, so Stop and
// kill reach everything it started, and has the kernel SIGTERM it when clown
// dies without unwinding (SIGKILL, an OOM kill), when the deferred Shutdown
// never runs. Start's caller must keep the forking thread alive (startPinned).
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// startPinned starts cmd from an OS thread locked for as long as the child
// runs, and returns the function that lets that thread go once the child has
// been waited for. Linux sends a Pdeathsig when the thread that forked the
// child exits, not the process, and the Go runtime ends a thread whose
// goroutine exits while locked to it; pinning keeps the signal for the death
// of clown itself (agentscope's MonitorRelay does the same).
func startPinned(cmd *exec.Cmd) (release func(), err error) {
	started := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		// Never unlocked: when this goroutine returns, the runtime ends the
		// thread, and by then the child has been reaped (or never started).
		runtime.LockOSThread()
		err := cmd.Start()
		started <- err
		if err != nil {
			return
		}
		<-done
	}()
	if err := <-started; err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }, nil
}

//go:build !linux

package pluginhost

import (
	"os/exec"
	"syscall"
)

// serverProcAttr puts a managed server in its own process group, so Stop and
// kill reach everything it started. Only Linux has a parent-death signal.
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// startPinned starts cmd; without a parent-death signal there is no thread to
// pin.
func startPinned(cmd *exec.Cmd) (release func(), err error) {
	return func() {}, cmd.Start()
}

//go:build linux

package worker

import (
	"os/exec"
	"syscall"
)

// setParentDeathSignal asks the kernel to send SIGTERM to the worker if the Go
// front door dies, including when it is killed with SIGKILL, so an abrupt exit
// does not leave an orphaned Node process behind.
func setParentDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

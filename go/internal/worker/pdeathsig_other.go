//go:build !linux

package worker

import "os/exec"

// setParentDeathSignal is a no-op where the kernel has no parent-death signal.
// The worker still exits on its own when it has no sessions; a front door that
// dies abruptly may leave it running until then.
func setParentDeathSignal(cmd *exec.Cmd) {}

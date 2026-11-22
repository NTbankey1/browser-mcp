//go:build linux

package worker

// keepWorkerAliveArgs disables the worker's own idle exit on Linux: the front
// door owns idle exit, and the kernel terminates the worker with the front
// door through Pdeathsig, including on SIGKILL.
func keepWorkerAliveArgs() []string {
	return []string{"--no-idle-exit"}
}

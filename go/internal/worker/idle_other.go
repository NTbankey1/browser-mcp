//go:build !linux

package worker

// keepWorkerAliveArgs leaves the worker's own idle exit in place where there
// is no parent-death signal: if the front door dies abruptly, the worker exits
// after its idle timeout instead of running forever.
func keepWorkerAliveArgs() []string {
	return nil
}

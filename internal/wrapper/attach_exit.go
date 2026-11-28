package wrapper

// exitObserver records a process's exit code while it is still alive so
// finalizeJob can tell a clean exit from a failure after the process is gone.
// Platforms that cannot observe non-child exit codes return nil from
// newExitObserver and keep the conservative unknown-exit failure.
type exitObserver interface {
	watch(pid int)
	exitCode(pid int) (int, bool)
	unwatch(pid int)
	close()
}

//go:build !darwin && !windows

package wrapper

// Linux cannot wait on processes it did not spawn, so a non-child exit code
// is not observable after the fact; keep the conservative unknown-exit path.
func newExitObserver() exitObserver { return nil }

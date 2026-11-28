//go:build !windows

package wrapper

import "context"

// Platform dispatch stubs; the runtime.GOOS guards in attach.go never call
// these on non-Windows systems.
func windowsProcessList(ctx context.Context, pids []int) ([]ProcessInfo, error) {
	return nil, nil
}

func windowsProcessAlive(pid int) bool { return false }

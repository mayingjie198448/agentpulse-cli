//go:build !linux && !darwin && !windows

package safety

import "fmt"

func openDirNoFollow(path string) (int, error) {
	return -1, fmt.Errorf("%w: directory-identity open is BLOCKED on this OS", ErrCleanupBlocked)
}

func fdPath(fd int) (string, error) {
	return "", fmt.Errorf("%w: kernel path lookup is BLOCKED on this OS", ErrCleanupBlocked)
}

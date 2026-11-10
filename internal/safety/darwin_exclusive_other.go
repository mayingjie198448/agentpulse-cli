//go:build !darwin

package safety

import "fmt"

// defaultDarwinExclusiveOS refuses exclusive Darwin sentinel creation on
// every non-Darwin platform. It does not return the caller path and it does
// not add a Linux or Windows creation backend.
func defaultDarwinExclusiveOS() darwinExclusiveOS {
	refuse := fmt.Errorf("%w: exclusive darwin sentinel creation is unavailable on this platform", ErrSentinelUnusable)
	return darwinExclusiveOS{
		OpenDirNoFollow: func(string) (int, error) { return -1, refuse },
		OpenExclusive:   func(int, string) (int, error) { return -1, refuse },
		Write:           func(int, []byte) error { return refuse },
		Fstat:           func(int) (darwinExclusiveFileID, error) { return darwinExclusiveFileID{}, refuse },
		FdPath:          func(int) (string, error) { return "", refuse },
		Close:           func(int) {},
	}
}

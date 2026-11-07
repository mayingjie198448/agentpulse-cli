//go:build !windows

package safety

import "fmt"

func recoverProfile(dir string, m RunManifest) (CleanupOutcome, error) {
	if !m.ProfileCreated {
		return CleanupOutcome{Result: "BLOCKED", Reason: "no owned profile"}, fmt.Errorf("%w: no owned profile", ErrCleanupBlocked)
	}
	return CleanupOutcome{Result: "BLOCKED", Reason: "profile recovery requires Windows"}, fmt.Errorf("%w: profile recovery requires Windows", ErrCleanupBlocked)
}

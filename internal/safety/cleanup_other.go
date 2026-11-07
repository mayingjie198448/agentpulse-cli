//go:build !linux

package safety

import "fmt"

// deleteOwned does not delete on platforms whose directory identity and
// reparse behavior were not verified. The root stays on disk.
func deleteOwned(rec RunRecord, homes []string) (CleanupOutcome, error) {
	return CleanupOutcome{
		Result: "BLOCKED",
		Reason: "cleanup identity walk is BLOCKED on this OS; root retained",
	}, fmt.Errorf("%w: cleanup identity walk is BLOCKED on this OS", ErrCleanupBlocked)
}

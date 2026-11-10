//go:build !windows && !darwin

package safety

import "os"

// createOwnedFixtureRoot creates the fixture directory. Non-Windows runs do
// not mint a Windows file-identity record.
func createOwnedFixtureRoot() (string, CreationRecord, error) {
	dir, err := os.MkdirTemp("", "agentpulse-fixture-")
	return dir, CreationRecord{}, err
}

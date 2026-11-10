//go:build darwin

package safety

import (
	"os"
	"path/filepath"
)

// createOwnedFixtureRoot creates the fixture directory on Darwin under /private/tmp.
func createOwnedFixtureRoot() (string, CreationRecord, error) {
	dir, err := os.MkdirTemp("/private/tmp", "agentpulse-fixture-")
	if err != nil {
		dir, err = os.MkdirTemp("", "agentpulse-fixture-")
		if err != nil {
			return "", CreationRecord{}, err
		}
	}
	canon, err := filepath.EvalSymlinks(dir)
	if err == nil && canon != "" {
		dir = canon
	}
	return dir, CreationRecord{}, nil
}

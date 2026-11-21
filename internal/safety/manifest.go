package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func manifestPath(dir, runID string) (string, error) {
	if !runIDRe.MatchString(runID) {
		return "", fmt.Errorf("%w: run id is not a recorded identifier", ErrCleanupBlocked)
	}
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("%w: registry directory is empty", ErrCleanupBlocked)
	}
	cleanDir := filepath.Clean(dir)
	path := filepath.Join(cleanDir, runID+".manifest.json")
	if filepath.Dir(path) != cleanDir {
		return "", fmt.Errorf("%w: manifest path escaped the registry", ErrCleanupBlocked)
	}
	return path, nil
}

func saveManifest(dir string, m RunManifest) error {
	path, err := manifestPath(dir, m.RunID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeJSON(path, m, 0600)
}

func loadManifest(dir, runID string) (RunManifest, error) {
	path, err := manifestPath(dir, runID)
	if err != nil {
		return RunManifest{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return RunManifest{}, err
	}
	var m RunManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return RunManifest{}, err
	}
	if m.RunID != runID || !runIDRe.MatchString(m.RunID) {
		return RunManifest{}, os.ErrInvalid
	}
	return m, nil
}

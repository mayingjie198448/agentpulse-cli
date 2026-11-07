package safety

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func manifestPath(dir, runID string) string {
	return filepath.Join(dir, runID+".manifest.json")
}

func saveManifest(dir string, m RunManifest) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeJSON(manifestPath(dir, m.RunID), m, 0600)
}

func loadManifest(dir, runID string) (RunManifest, error) {
	b, err := os.ReadFile(manifestPath(dir, runID))
	if err != nil {
		return RunManifest{}, err
	}
	var m RunManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return RunManifest{}, err
	}
	if m.RunID != runID {
		return RunManifest{}, os.ErrInvalid
	}
	return m, nil
}

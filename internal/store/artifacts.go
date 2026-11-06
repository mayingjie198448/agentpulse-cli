package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

// CleanJobArtifacts removes local artifacts created for a specific job.
// It inspects statusFile (defaults to .agentpulse/status.json if empty):
//   - If the status snapshot matches jobID, it clears the Job info while preserving the machine snapshot.
//   - It removes any per-job files or subdirectories under the status directory keyed by jobID.
//
// It never removes the status directory itself, user project files, git repos, or machine config.
func CleanJobArtifacts(jobID string, statusFile string) error {
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	if statusFile == "" {
		statusFile = ".agentpulse/status.json"
	}

	// 1. Update status snapshot file if it exists and references this job
	if data, err := os.ReadFile(statusFile); err == nil {
		var st model.LocalStatus
		if err := json.Unmarshal(data, &st); err == nil {
			if st.Job.ID == jobID {
				st.Job = model.Job{}
				st.PID = 0
				st.UpdatedAt = time.Now().UTC()
				if updatedData, err := json.MarshalIndent(st, "", "  "); err == nil {
					tmpFile := statusFile + fmt.Sprintf(".tmp.%d", time.Now().UnixNano())
					if err := os.WriteFile(tmpFile, updatedData, 0644); err == nil {
						_ = os.Rename(tmpFile, statusFile)
					}
				}
			}
		}
	}

	// 2. Scan status directory (.agentpulse) for per-job files or folders
	dir := filepath.Dir(statusFile)
	if dir != "" && dir != "." {
		entries, err := os.ReadDir(dir)
		if err == nil {
			baseName := filepath.Base(statusFile)
			for _, entry := range entries {
				// Do not delete the status snapshot file itself here
				if entry.Name() == baseName {
					continue
				}
				// If an entry name contains jobID, remove it
				if strings.Contains(entry.Name(), jobID) {
					_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
					continue
				}
				// If there is a subfolder named "jobs", check inside it
				if entry.IsDir() && entry.Name() == "jobs" {
					jobsDir := filepath.Join(dir, "jobs")
					if subEntries, err := os.ReadDir(jobsDir); err == nil {
						for _, sub := range subEntries {
							if strings.Contains(sub.Name(), jobID) {
								_ = os.RemoveAll(filepath.Join(jobsDir, sub.Name()))
							}
						}
					}
				}
			}
		}
	}

	return nil
}

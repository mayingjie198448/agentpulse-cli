package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
)

func TestCleanJobArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	statusDir := filepath.Join(tmpDir, ".agentpulse")
	if err := os.MkdirAll(statusDir, 0755); err != nil {
		t.Fatalf("failed to create status dir: %v", err)
	}

	statusFile := filepath.Join(statusDir, "status.json")

	// 1. Setup status.json with job-target
	initialStatus := model.LocalStatus{
		Machine: model.Machine{
			ID:     "mac-test",
			Name:   "Mac Test",
			Online: true,
		},
		Job: model.Job{
			ID:        "job-target",
			MachineID: "mac-test",
			Status:    model.StatusDone,
			Title:     "Target Job Title",
		},
		PID:       12345,
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(initialStatus, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal initial status: %v", err)
	}
	if err := os.WriteFile(statusFile, data, 0644); err != nil {
		t.Fatalf("failed to write status file: %v", err)
	}

	// 2. Setup per-job files and subdirectories
	targetMeta := filepath.Join(statusDir, "job-target.meta")
	if err := os.WriteFile(targetMeta, []byte("metadata"), 0644); err != nil {
		t.Fatalf("failed to write target meta: %v", err)
	}

	jobsSubdir := filepath.Join(statusDir, "jobs")
	if err := os.MkdirAll(jobsSubdir, 0755); err != nil {
		t.Fatalf("failed to create jobs subdir: %v", err)
	}
	targetJobFile := filepath.Join(jobsSubdir, "job-target.log")
	if err := os.WriteFile(targetJobFile, []byte("log data"), 0644); err != nil {
		t.Fatalf("failed to write target log: %v", err)
	}

	otherJobFile := filepath.Join(jobsSubdir, "job-other.log")
	if err := os.WriteFile(otherJobFile, []byte("other log data"), 0644); err != nil {
		t.Fatalf("failed to write other log: %v", err)
	}

	otherMeta := filepath.Join(statusDir, "job-other.meta")
	if err := os.WriteFile(otherMeta, []byte("other metadata"), 0644); err != nil {
		t.Fatalf("failed to write other meta: %v", err)
	}

	// 3. Clean artifacts for job-target
	if err := store.CleanJobArtifacts("job-target", statusFile); err != nil {
		t.Fatalf("CleanJobArtifacts error: %v", err)
	}

	// Verify target files are gone
	if _, err := os.Stat(targetMeta); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted, but still exists", targetMeta)
	}
	if _, err := os.Stat(targetJobFile); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted, but still exists", targetJobFile)
	}

	// Verify other files and statusDir still exist
	if _, err := os.Stat(otherMeta); err != nil {
		t.Errorf("expected %s to be preserved, got err: %v", otherMeta, err)
	}
	if _, err := os.Stat(otherJobFile); err != nil {
		t.Errorf("expected %s to be preserved, got err: %v", otherJobFile, err)
	}
	if _, err := os.Stat(statusDir); err != nil {
		t.Errorf("expected statusDir %s to be preserved, got err: %v", statusDir, err)
	}

	// Verify status.json updated: job cleared, machine intact
	updatedRaw, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatalf("failed to read updated status file: %v", err)
	}
	var updated model.LocalStatus
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("failed to parse updated status: %v", err)
	}
	if updated.Machine.ID != "mac-test" || !updated.Machine.Online {
		t.Errorf("machine info was corrupted: %+v", updated.Machine)
	}
	if updated.Job.ID != "" || updated.PID != 0 {
		t.Errorf("expected job and PID to be cleared, got job=%+v, pid=%d", updated.Job, updated.PID)
	}
}

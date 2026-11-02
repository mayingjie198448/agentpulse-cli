package mock_test

import (
	"context"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/mock"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

func TestMockStore_ListMachines(t *testing.T) {
	st := mock.NewMockStore()
	defer st.Close()

	ctx := context.Background()
	machines, err := st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(machines) != 4 {
		t.Fatalf("expected 4 machines, got %d", len(machines))
	}

	expectedIDs := map[string]bool{
		"ccdq":     false,
		"mac-mini": false,
		"home-pc":  false,
		"macbook":  false,
	}

	for _, m := range machines {
		if _, ok := expectedIDs[m.ID]; ok {
			expectedIDs[m.ID] = true
		}
	}

	for id, found := range expectedIDs {
		if !found {
			t.Errorf("machine %s not found in list", id)
		}
	}
}

func TestMockStore_ListJobs(t *testing.T) {
	st := mock.NewMockStore()
	defer st.Close()

	ctx := context.Background()
	jobs, err := st.ListJobs(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(jobs) == 0 {
		t.Fatalf("expected jobs, got none")
	}

	var hasAgy, hasOpenCode bool
	var hasRunning, hasDone, hasFailed bool

	for _, j := range jobs {
		if j.Engine == model.EngineAgy {
			hasAgy = true
		}
		if j.Engine == model.EngineOpenCode {
			hasOpenCode = true
		}
		if j.Status == model.StatusRunning {
			hasRunning = true
		}
		if j.Status == model.StatusDone {
			hasDone = true
		}
		if j.Status == model.StatusFailed {
			hasFailed = true
		}
	}

	if !hasAgy || !hasOpenCode {
		t.Errorf("expected both agy and opencode jobs, got agy=%v, opencode=%v", hasAgy, hasOpenCode)
	}
	if !hasRunning || !hasDone || !hasFailed {
		t.Errorf("expected running, done, and failed jobs: running=%v, done=%v, failed=%v", hasRunning, hasDone, hasFailed)
	}
}

func TestMockStore_GetJobAndLogs(t *testing.T) {
	st := mock.NewMockStore()
	defer st.Close()

	ctx := context.Background()
	job, err := st.GetJob(ctx, "job-agy-01")
	if err != nil {
		t.Fatalf("unexpected error getting job: %v", err)
	}
	if job.ID != "job-agy-01" {
		t.Errorf("expected job ID job-agy-01, got %s", job.ID)
	}

	// Test non-existent job
	_, err = st.GetJob(ctx, "non-existent-id")
	if err == nil {
		t.Errorf("expected error for non-existent job, got nil")
	}

	// Test getting logs
	logs, err := st.GetLogs(ctx, "job-agy-01", 3)
	if err != nil {
		t.Fatalf("unexpected error getting logs: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("expected 3 log entries, got %d", len(logs))
	}
}

func TestMockStore_Subscribe(t *testing.T) {
	st := mock.NewMockStore()
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	subCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("unexpected error subscribing: %v", err)
	}

	select {
	case ev, ok := <-subCh:
		if !ok {
			t.Fatalf("channel closed unexpectedly")
		}
		if ev.Type == "" {
			t.Errorf("expected event type to be populated")
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for mock event")
	}
}

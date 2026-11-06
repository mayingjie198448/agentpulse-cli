package model_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

func TestMachineHeartbeatAgo(t *testing.T) {
	now := time.Now()

	m1 := model.Machine{LastHeartbeat: now.Add(-5 * time.Second)}
	if m1.HeartbeatAgo() != "5s ago" {
		t.Errorf("expected '5s ago', got %s", m1.HeartbeatAgo())
	}

	m2 := model.Machine{LastHeartbeat: now.Add(-12 * time.Minute)}
	if m2.HeartbeatAgo() != "12m ago" {
		t.Errorf("expected '12m ago', got %s", m2.HeartbeatAgo())
	}

	m3 := model.Machine{LastHeartbeat: now.Add(-2 * time.Hour)}
	if m3.HeartbeatAgo() != "2h ago" {
		t.Errorf("expected '2h ago', got %s", m3.HeartbeatAgo())
	}
}

func TestDispatchCommandJSON(t *testing.T) {
	ts, _ := time.Parse(time.RFC3339, "2026-11-02T13:32:00Z")
	cmd := model.DispatchCommand{
		CommandID:      "cmd-88ab12-34ef",
		JobID:          "job-20261102-003",
		Engine:         model.EngineAgy,
		Cwd:            "/data/workspace/backend-api",
		Command:        "agy",
		Args:           []string{"run", "--prompt", "Optimize database query pool"},
		Title:          "Optimize DB pool in backend-api",
		TimeoutSeconds: 3600,
		Env: map[string]string{
			"ENV":       "production_test",
			"LOG_LEVEL": "debug",
		},
		RequestedBy: "user@laptop",
		Timestamp:   ts,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("failed to marshal DispatchCommand: %v", err)
	}

	var decoded model.DispatchCommand
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal DispatchCommand: %v", err)
	}

	if decoded.CommandID != cmd.CommandID || decoded.JobID != cmd.JobID || decoded.Engine != cmd.Engine {
		t.Errorf("decoded struct mismatch: %+v", decoded)
	}
	if decoded.Env["ENV"] != "production_test" {
		t.Errorf("env mismatch: %v", decoded.Env)
	}
	if !decoded.Timestamp.Equal(ts) {
		t.Errorf("timestamp mismatch: %v vs %v", decoded.Timestamp, ts)
	}
}

func TestMachineAnnounce(t *testing.T) {
	ann := model.MachineAnnounce{
		V:         1,
		MachineID: "macbook",
		TS:        time.Now().UTC(),
		Rev:       1,
		Hostname:  "macbook-pro",
		OS:        "darwin/arm64",
		Agents: []model.AgentAnnounce{
			{
				ID:     "agy",
				Alias:  []string{"agy"},
				Ok:     true,
				Detail: "/usr/local/bin/agy",
			},
			{
				ID:     "opencode",
				Alias:  []string{"op", "opencode"},
				Ok:     false,
				Detail: "not found",
			},
		},
	}

	data, err := json.Marshal(ann)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var restored model.MachineAnnounce
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if restored.MachineID != "macbook" || restored.Rev != 1 {
		t.Errorf("unexpected restored announce: %+v", restored)
	}

	ag, ok := restored.FindAgent("op")
	if !ok || ag.ID != "opencode" {
		t.Errorf("expected to find agent 'opencode' via alias 'op', got %+v, ok=%v", ag, ok)
	}

	ag2, ok2 := restored.FindAgent("AGY")
	if !ok2 || !ag2.Ok {
		t.Errorf("expected to find agent 'agy' case-insensitively, got %+v, ok=%v", ag2, ok2)
	}

	_, ok3 := restored.FindAgent("unknown")
	if ok3 {
		t.Error("expected unknown agent to return false")
	}
}

func TestJob_NeedsAttention(t *testing.T) {
	tests := []struct {
		name string
		job  model.Job
		want bool
	}{
		{
			name: "running normal",
			job:  model.Job{Status: model.StatusRunning, Progress: 50},
			want: false,
		},
		{
			name: "done normal",
			job:  model.Job{Status: model.StatusDone, Progress: 100},
			want: false,
		},
		{
			name: "failed status",
			job:  model.Job{Status: model.StatusFailed, Error: "process exited with code 1"},
			want: true,
		},
		{
			name: "waiting input status",
			job:  model.Job{Status: "waiting_input", Title: "Need confirmation"},
			want: true,
		},
		{
			name: "stuck status",
			job:  model.Job{Status: "stuck"},
			want: true,
		},
		{
			name: "waiting status",
			job:  model.Job{Status: "waiting"},
			want: true,
		},
		{
			name: "running with unexpected error message",
			job:  model.Job{Status: model.StatusRunning, Error: "network timeout during tool execution"},
			want: true,
		},
		{
			name: "queued normal",
			job:  model.Job{Status: model.StatusQueued},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.job.NeedsAttention()
			if got != tt.want {
				t.Errorf("job.NeedsAttention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJob_ExtendedFieldsAndJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ended := now.Add(45 * time.Minute)

	j := model.Job{
		ID:          "job-test-001",
		MachineID:   "mac-studio",
		Engine:      model.EngineAgy,
		Status:      model.StatusDone,
		Progress:    100.0,
		Cwd:         "/Users/mayingjie/Repos/agentpulse-cli",
		Command:     "agy run --prompt 'refactor store'",
		Title:       "Refactor store layer",
		Repo:        "mayingjie198448/agentpulse-cli",
		Project:     "agentpulse-cli",
		Prompt:      "Refactor store layer for MQTT integration",
		StartedAt:   now,
		UpdatedAt:   ended,
		EndedAt:     ended,
		DurationStr: "45m 0s",
		RequestedBy: "mayingjie@mac-studio",
		Error:       "",
	}

	if j.RepoOrProject() != "mayingjie198448/agentpulse-cli" {
		t.Errorf("expected repo 'mayingjie198448/agentpulse-cli', got %q", j.RepoOrProject())
	}
	if j.DisplayTitle() != "Refactor store layer" {
		t.Errorf("expected title 'Refactor store layer', got %q", j.DisplayTitle())
	}

	jOnlyProj := model.Job{Project: "subproject"}
	if jOnlyProj.RepoOrProject() != "subproject" {
		t.Errorf("expected project fallback 'subproject', got %q", jOnlyProj.RepoOrProject())
	}

	jNoTitle := model.Job{ID: "job-fallback-id"}
	if jNoTitle.DisplayTitle() != "job-fallback-id" {
		t.Errorf("expected ID fallback 'job-fallback-id', got %q", jNoTitle.DisplayTitle())
	}

	data, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded model.Job
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if decoded.Repo != j.Repo || decoded.Project != j.Project || decoded.Prompt != j.Prompt {
		t.Errorf("decoded fields mismatch: repo=%q project=%q prompt=%q", decoded.Repo, decoded.Project, decoded.Prompt)
	}
	if decoded.RequestedBy != j.RequestedBy || !decoded.EndedAt.Equal(j.EndedAt) {
		t.Errorf("decoded meta mismatch: requested_by=%q ended_at=%v", decoded.RequestedBy, decoded.EndedAt)
	}
}

func TestJob_BackwardCompatibility(t *testing.T) {
	// Legacy JSON payload from v0.7.0 without repo, project, prompt, ended_at, requested_by
	legacyJSON := `{
		"id": "job-legacy-001",
		"machine_id": "mac-node",
		"engine": "agy",
		"status": "running",
		"progress": 55.0,
		"cwd": "/tmp",
		"command": "agy",
		"title": "Legacy Job",
		"started_at": "2026-11-06T10:00:00Z",
		"updated_at": "2026-11-06T10:05:00Z"
	}`

	var j model.Job
	if err := json.Unmarshal([]byte(legacyJSON), &j); err != nil {
		t.Fatalf("failed to unmarshal legacy job JSON: %v", err)
	}

	if j.ID != "job-legacy-001" || j.Title != "Legacy Job" {
		t.Errorf("unexpected legacy job fields: %+v", j)
	}
	if j.Repo != "" || j.Project != "" || j.Prompt != "" || j.RequestedBy != "" {
		t.Errorf("expected omitted extended fields to be empty, got repo=%q proj=%q prompt=%q req=%q",
			j.Repo, j.Project, j.Prompt, j.RequestedBy)
	}
	if !j.EndedAt.IsZero() {
		t.Errorf("expected ended_at to be zero for running legacy job, got %v", j.EndedAt)
	}
	if j.RepoOrProject() != "" {
		t.Errorf("expected empty RepoOrProject(), got %q", j.RepoOrProject())
	}
}

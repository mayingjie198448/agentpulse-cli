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

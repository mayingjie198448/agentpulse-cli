package model_test

import (
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

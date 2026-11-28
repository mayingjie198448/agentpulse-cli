package autostart

import (
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestPublishOffline_EmptyInputs(t *testing.T) {
	// Should cleanly no-op without error if URL or machineID is empty
	if err := PublishOffline(config.BrokerConfig{URL: ""}, "node-1"); err != nil {
		t.Errorf("expected no-op nil error for empty URL, got %v", err)
	}
	if err := PublishOffline(config.BrokerConfig{URL: "tcp://127.0.0.1:1883"}, ""); err != nil {
		t.Errorf("expected no-op nil error for empty machineID, got %v", err)
	}
}

func TestServiceLabelDefaultsAndOverrides(t *testing.T) {
	// 1. Defaults
	if GetServiceLabel() != ServiceLabel {
		t.Errorf("expected default label %q, got %q", ServiceLabel, GetServiceLabel())
	}
	if GetPlistFileName() != PlistFileName {
		t.Errorf("expected default plist %q, got %q", PlistFileName, GetPlistFileName())
	}

	// 2. Programmatic override via SetTestServiceLabel
	reset := SetTestServiceLabel("com.agentpulse.test.custom", "")
	if GetServiceLabel() != "com.agentpulse.test.custom" {
		t.Errorf("expected custom label, got %q", GetServiceLabel())
	}
	if GetPlistFileName() != "com.agentpulse.test.custom.plist" {
		t.Errorf("expected auto-derived plist filename, got %q", GetPlistFileName())
	}
	reset()

	// Restored to default
	if GetServiceLabel() != ServiceLabel {
		t.Errorf("expected restored default label %q, got %q", ServiceLabel, GetServiceLabel())
	}

	// 3. Environment variable override
	t.Setenv("AGENTPULSE_TEST_SERVICE_LABEL", "com.agentpulse.test.fromenv")
	if GetServiceLabel() != "com.agentpulse.test.fromenv" {
		t.Errorf("expected env label, got %q", GetServiceLabel())
	}
	if GetPlistFileName() != "com.agentpulse.test.fromenv.plist" {
		t.Errorf("expected derived plist filename from env label, got %q", GetPlistFileName())
	}

	t.Setenv("AGENTPULSE_TEST_PLIST_FILENAME", "custom-explicit.plist")
	if GetPlistFileName() != "custom-explicit.plist" {
		t.Errorf("expected explicit plist filename from env, got %q", GetPlistFileName())
	}
}

package autostart

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestPublishOffline_EmptyInputs(t *testing.T) {
	// Should cleanly no-op without error if URL or machineID is empty.
	// Non-empty broker URLs are not exercised here; that path would open a network client.
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

// TestAutostartSupportMatchesPlatform checks the support bit on every OS.
// Non-darwin Enable/Status/Disable are the in-tree placeholders and do not
// touch a LaunchAgent directory. Darwin plist and launchctl behavior stays in
// autostart_darwin_test.go so this file does not reference those symbols.
func TestAutostartSupportMatchesPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" {
		if !IsSupported() {
			t.Fatal("macOS should report autostart supported")
		}
		return
	}

	if IsSupported() {
		t.Fatalf("%s should not report autostart supported", runtime.GOOS)
	}
	err := Enable(&config.Config{
		MachineID: "qa-autostart-platform",
		Autostart: true,
	}, filepath.Join(t.TempDir(), "agentpulse"))
	if err == nil {
		t.Fatal("Enable on this platform should return an error")
	}
	if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Fatalf("Enable error should name the platform, got %v", err)
	}

	st := Status()
	if st.Supported || st.Installed || st.Running {
		t.Fatalf("unsupported platform status = %+v", st)
	}
	if !strings.Contains(st.Detail, runtime.GOOS) {
		t.Fatalf("status detail should name the platform, got %q", st.Detail)
	}
	if err := Disable(); err != nil {
		t.Fatalf("Disable on unsupported platform: %v", err)
	}
}

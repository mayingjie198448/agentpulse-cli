package autostart

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestPlistGeneration(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("skipping macOS plist test on non-darwin platform")
	}

	content := GeneratePlistContent("/usr/local/bin/agentpulse", "/Users/tester", "/Users/tester/Library/Logs/agentpulse")
	if !strings.Contains(content, ServiceLabel) {
		t.Errorf("expected plist to contain service label %q", ServiceLabel)
	}
	if !strings.Contains(content, "<string>/usr/local/bin/agentpulse</string>") {
		t.Errorf("expected plist to contain executable path")
	}
	if !strings.Contains(content, "<string>listen</string>") {
		t.Errorf("expected plist to specify 'listen' argument")
	}
	if !strings.Contains(content, "<key>RunAtLoad</key>") || !strings.Contains(content, "<true/>") {
		t.Errorf("expected plist to specify RunAtLoad=true")
	}
	if !strings.Contains(content, "<key>KeepAlive</key>") {
		t.Errorf("expected plist to specify KeepAlive=true")
	}
	if !strings.Contains(content, "/Users/tester/Library/Logs/agentpulse/daemon.out.log") {
		t.Errorf("expected standard stdout path")
	}
}

func TestEnableAndDisable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("skipping macOS LaunchAgent enable/disable test on non-darwin")
	}

	tmpLaunchDir := t.TempDir()
	tmpLogsDir := t.TempDir()

	testLaunchAgentsDir = tmpLaunchDir
	testLogsDir = tmpLogsDir
	defer func() {
		testLaunchAgentsDir = ""
		testLogsDir = ""
	}()

	// Mock execCommand to avoid running real launchctl in unit test
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	cfg := &config.Config{
		MachineID: "test-node",
		Autostart: true,
	}

	// 1. Enable
	err := Enable(cfg, "/usr/local/bin/agentpulse")
	if err != nil {
		t.Fatalf("Enable failed: %v", err)
	}

	plistPath := filepath.Join(tmpLaunchDir, PlistFileName)
	info, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("expected plist file to be created at %q, got error: %v", plistPath, err)
	}
	if info.Size() == 0 {
		t.Fatalf("plist file is empty")
	}

	st := Status()
	if !st.Installed {
		t.Errorf("expected Status().Installed=true, got false")
	}

	// 2. Disable
	if err := Disable(); err != nil {
		t.Fatalf("Disable failed: %v", err)
	}

	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Errorf("expected plist file to be removed after Disable(), but it still exists")
	}

	st = Status()
	if st.Installed {
		t.Errorf("expected Status().Installed=false after Disable(), got true")
	}
}

func TestEnable_FailureWhenLaunchctlFails(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("skipping macOS LaunchAgent enable test on non-darwin")
	}

	tmpLaunchDir := t.TempDir()
	tmpLogsDir := t.TempDir()

	testLaunchAgentsDir = tmpLaunchDir
	testLogsDir = tmpLogsDir
	defer func() {
		testLaunchAgentsDir = ""
		testLogsDir = ""
	}()

	// Mock execCommand to always fail for bootstrap and load
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}
	defer func() { execCommand = oldExec }()

	cfg := &config.Config{
		MachineID: "test-node",
		Autostart: true,
	}

	err := Enable(cfg, "/usr/local/bin/agentpulse")
	if err == nil {
		t.Fatalf("expected Enable to fail when all launchctl bootstrap/load commands fail, got nil")
	}
	if !strings.Contains(err.Error(), "all launchctl attempts failed") {
		t.Errorf("expected descriptive error mentioning launchctl attempts, got: %v", err)
	}
}

func TestPublishOffline_EmptyInputs(t *testing.T) {
	// Should cleanly no-op without error if URL or machineID is empty
	if err := PublishOffline(config.BrokerConfig{URL: ""}, "node-1"); err != nil {
		t.Errorf("expected no-op nil error for empty URL, got %v", err)
	}
	if err := PublishOffline(config.BrokerConfig{URL: "tcp://127.0.0.1:1883"}, ""); err != nil {
		t.Errorf("expected no-op nil error for empty machineID, got %v", err)
	}
}

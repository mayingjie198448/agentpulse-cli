//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestPlistGeneration(t *testing.T) {
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

func TestDifferentLabelsDoNotBootoutEachOther(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	tmpLaunchDir := t.TempDir()
	tmpLogsDir := t.TempDir()

	testLaunchAgentsDir = tmpLaunchDir
	testLogsDir = tmpLogsDir
	defer func() {
		testLaunchAgentsDir = ""
		testLogsDir = ""
	}()

	var executedCmds []string
	oldExec := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		executedCmds = append(executedCmds, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
		return exec.Command("true")
	}
	defer func() { execCommand = oldExec }()

	cfg := &config.Config{MachineID: "test-node", Autostart: true}

	// Step 1: Install production / Label A (com.agentpulse.daemon)
	labelA := ServiceLabel
	resetLabelA := SetTestServiceLabel(labelA, "")
	defer resetLabelA()

	if err := Enable(cfg, "/usr/local/bin/agentpulse"); err != nil {
		t.Fatalf("Enable Label A failed: %v", err)
	}

	plistPathA := filepath.Join(tmpLaunchDir, PlistFileName)
	if _, err := os.Stat(plistPathA); err != nil {
		t.Fatalf("expected plist A to exist at %q", plistPathA)
	}

	// Clear recorded commands before operating on Label B
	executedCmds = nil

	// Step 2: Switch to isolated test Label B (com.agentpulse.daemon.test-isolate)
	labelB := "com.agentpulse.daemon.test-isolate"
	resetLabelB := SetTestServiceLabel(labelB, "")
	defer resetLabelB()

	if GetServiceLabel() != labelB {
		t.Fatalf("expected GetServiceLabel to be %q, got %q", labelB, GetServiceLabel())
	}
	expectedPlistBName := labelB + ".plist"
	if GetPlistFileName() != expectedPlistBName {
		t.Fatalf("expected GetPlistFileName to be %q, got %q", expectedPlistBName, GetPlistFileName())
	}

	// Enable Label B
	if err := Enable(cfg, "/usr/local/bin/agentpulse"); err != nil {
		t.Fatalf("Enable Label B failed: %v", err)
	}

	plistPathB := filepath.Join(tmpLaunchDir, expectedPlistBName)
	if _, err := os.Stat(plistPathB); err != nil {
		t.Fatalf("expected plist B to exist at %q", plistPathB)
	}

	// Verify that Label B operations NEVER called bootout on Label A!
	cmdsForBEnable := make([]string, len(executedCmds))
	copy(cmdsForBEnable, executedCmds)
	executedCmds = nil

	for _, cmd := range cmdsForBEnable {
		if strings.Contains(cmd, "bootout") {
			// Check exact target label at the end of the domain target path (e.g. gui/501/com.agentpulse.daemon)
			if strings.HasSuffix(cmd, "/"+labelA) {
				t.Fatalf("VIOLATION: bootout command during Label B enable targeted Label A: %s", cmd)
			}
			if !strings.HasSuffix(cmd, "/"+labelB) {
				t.Errorf("expected bootout command to target Label B, got: %s", cmd)
			}
		}
	}

	// Verify Label A plist is still completely intact on disk
	if _, err := os.Stat(plistPathA); err != nil {
		t.Fatalf("expected plist A to remain intact after Label B enable, but stat failed: %v", err)
	}

	// Step 3: Disable Label B
	if err := Disable(); err != nil {
		t.Fatalf("Disable Label B failed: %v", err)
	}

	cmdsForBDisable := make([]string, len(executedCmds))
	copy(cmdsForBDisable, executedCmds)
	executedCmds = nil

	for _, cmd := range cmdsForBDisable {
		if strings.Contains(cmd, "bootout") {
			if strings.HasSuffix(cmd, "/"+labelA) {
				t.Fatalf("VIOLATION: bootout during Label B disable targeted Label A: %s", cmd)
			}
			if !strings.HasSuffix(cmd, "/"+labelB) {
				t.Errorf("expected bootout during Label B disable to target Label B, got: %s", cmd)
			}
		}
		if strings.Contains(cmd, "unload") {
			if strings.Contains(cmd, plistPathA) {
				t.Fatalf("VIOLATION: unload during Label B disable targeted plist A: %s", cmd)
			}
		}
	}

	// Verify Label B plist was removed, but Label A plist is STILL present and untouched!
	if _, err := os.Stat(plistPathB); !os.IsNotExist(err) {
		t.Errorf("expected plist B to be removed, but still exists")
	}
	if _, err := os.Stat(plistPathA); err != nil {
		t.Errorf("expected plist A to still exist unharmed after Label B disable, got: %v", err)
	}
}

// Regression test for FAIL3:
// When bootstrap gui & user fail with exit status 5, and legacy load exits 0 but prints
// "Load failed: 5: Input/output error", Enable MUST NOT treat this as success; it must return a non-nil error.
func TestEnable_BootstrapFailsAndLoadOutputsErrorWithExit0(t *testing.T) {
	tmpLaunchDir := t.TempDir()
	tmpLogsDir := t.TempDir()
	testLaunchAgentsDir = tmpLaunchDir
	testLogsDir = tmpLogsDir
	defer func() {
		testLaunchAgentsDir = ""
		testLogsDir = ""
	}()

	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		if len(args) == 0 {
			return exec.Command("true")
		}
		switch args[0] {
		case "bootout":
			// Bootout exits with code 3 when process not found
			return exec.Command("sh", "-c", "echo 'Boot-out failed: 3: No such process' >&2; exit 3")
		case "bootstrap":
			// Bootstrap fails with exit status 5
			return exec.Command("sh", "-c", "echo 'Bootstrap failed: 5: Input/output error' >&2; exit 5")
		case "load":
			// Legacy load prints failure message but exits with code 0!
			return exec.Command("sh", "-c", "echo 'Load failed: 5: Input/output error'; exit 0")
		case "print":
			return exec.Command("sh", "-c", "echo 'Bad request. Could not find service' >&2; exit 113")
		case "list":
			return exec.Command("sh", "-c", "echo 'Could not find service'; exit 1")
		default:
			return exec.Command("true")
		}
	}

	cfg := &config.Config{
		MachineID: "test-fail3-node",
		Autostart: true,
	}

	err := Enable(cfg, "/usr/local/bin/agentpulse")
	if err == nil {
		t.Fatalf("CRITICAL BUG: Enable returned nil when bootstrap failed and load exited 0 with 'Load failed' output!")
	}
	if !strings.Contains(err.Error(), "all launchctl attempts failed") && !strings.Contains(err.Error(), "Load failed") {
		t.Errorf("expected error message to report launchctl failure, got: %v", err)
	}
}

// Regression test for FAIL3:
// When launchctl attempts appear to succeed but the service label is not actually registered in launchd domain,
// Enable must return an error rather than false success.
func TestEnable_ServiceNotRegisteredFails(t *testing.T) {
	tmpLaunchDir := t.TempDir()
	tmpLogsDir := t.TempDir()
	testLaunchAgentsDir = tmpLaunchDir
	testLogsDir = tmpLogsDir
	defer func() {
		testLaunchAgentsDir = ""
		testLogsDir = ""
	}()

	oldExec := execCommand
	defer func() { execCommand = oldExec }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		if len(args) == 0 {
			return exec.Command("true")
		}
		switch args[0] {
		case "bootout":
			return exec.Command("true")
		case "bootstrap":
			// Bootstrap fails
			return exec.Command("sh", "-c", "exit 1")
		case "load":
			// Load exits 0 with no output (pretending success)
			return exec.Command("true")
		case "print":
			// Service not found in domain
			return exec.Command("sh", "-c", "echo 'Bad request. Could not find service' >&2; exit 113")
		case "list":
			// Service not found in list
			return exec.Command("sh", "-c", "echo 'Could not find service'; exit 1")
		default:
			return exec.Command("true")
		}
	}

	cfg := &config.Config{
		MachineID: "test-unregistered-node",
		Autostart: true,
	}

	err := Enable(cfg, "/usr/local/bin/agentpulse")
	if err == nil {
		t.Fatalf("expected Enable to fail when service is not registered in launchd domain, got nil")
	}
	if !strings.Contains(err.Error(), "not registered in launchd domain") {
		t.Errorf("expected error to indicate service not registered, got: %v", err)
	}
}

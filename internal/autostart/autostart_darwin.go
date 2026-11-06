//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

var (
	// Test hooks for directory and command overrides
	testLaunchAgentsDir string
	testLogsDir         string
	execCommand         = exec.Command
)

// SetTestOverrides allows unit tests to redirect LaunchAgents/Logs dirs to temp dirs
// and mock exec.Command without affecting the user's real LaunchAgent.
func SetTestOverrides(launchAgentsDir, logsDir string, cmdFn func(name string, args ...string) *exec.Cmd) func() {
	oldLaunch := testLaunchAgentsDir
	oldLogs := testLogsDir
	oldExec := execCommand
	testLaunchAgentsDir = launchAgentsDir
	testLogsDir = logsDir
	if cmdFn != nil {
		execCommand = cmdFn
	}
	return func() {
		testLaunchAgentsDir = oldLaunch
		testLogsDir = oldLogs
		execCommand = oldExec
	}
}

// IsSupported returns true on macOS where LaunchAgents are supported.
func IsSupported() bool {
	return true
}

func getLaunchAgentsDir() string {
	if testLaunchAgentsDir != "" {
		return testLaunchAgentsDir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}

func getLogsDir() string {
	if testLogsDir != "" {
		return testLogsDir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, "Library", "Logs", "agentpulse")
}

// GeneratePlistContent formats the LaunchAgent XML plist string.
func GeneratePlistContent(exePath, home, logsDir string) string {
	pathEnv := os.Getenv("PATH")
	standardPaths := "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if pathEnv == "" {
		pathEnv = fmt.Sprintf("%s/.local/bin:%s", home, standardPaths)
	} else if !strings.Contains(pathEnv, "/opt/homebrew/bin") {
		pathEnv = fmt.Sprintf("/opt/homebrew/bin:%s/.local/bin:%s", home, pathEnv)
	}

	stdoutPath := filepath.Join(logsDir, "daemon.out.log")
	stderrPath := filepath.Join(logsDir, "daemon.err.log")

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>listen</string>
	</array>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>%s</string>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, GetServiceLabel(), exePath, home, home, pathEnv, stdoutPath, stderrPath)
}

// Enable installs the LaunchAgent plist file and bootstraps it via launchctl.
func Enable(cfg *config.Config, exePath string) error {
	bin, err := resolveExecutable(exePath)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}

	launchDir := getLaunchAgentsDir()
	if err := os.MkdirAll(launchDir, 0755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents directory: %w", err)
	}

	logsDir := getLogsDir()
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	plistPath := filepath.Join(launchDir, GetPlistFileName())
	content := GeneratePlistContent(bin, home, logsDir)

	if err := os.WriteFile(plistPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write LaunchAgent plist %q: %w", plistPath, err)
	}

	// Bootstrap service as current user
	uid := os.Getuid()
	label := GetServiceLabel()

	// Best-effort bootout first to clean up any previous instance of this specific label
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, label)).Run()
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("user/%d/%s", uid, label)).Run()

	// 1. Try bootstrap in gui domain
	cmdGui := execCommand("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), plistPath)
	outGui, errGui := cmdGui.CombinedOutput()
	guiSuccess := errGui == nil && !isLaunchctlFailure(outGui)

	if !guiSuccess {
		// 2. Fall back to user domain (e.g. headless/ssh session)
		cmdUser := execCommand("launchctl", "bootstrap", fmt.Sprintf("user/%d", uid), plistPath)
		outUser, errUser := cmdUser.CombinedOutput()
		userSuccess := errUser == nil && !isLaunchctlFailure(outUser)

		if !userSuccess {
			// 3. Legacy fallback to load -w
			cmdLoad := execCommand("launchctl", "load", "-w", plistPath)
			outLoad, errLoad := cmdLoad.CombinedOutput()
			if errLoad != nil || isLaunchctlFailure(outLoad) {
				return fmt.Errorf("failed to enable LaunchAgent: all launchctl attempts failed (bootstrap gui: %v [%s]; bootstrap user: %v [%s]; load: %v [%s])",
					errGui, strings.TrimSpace(string(outGui)),
					errUser, strings.TrimSpace(string(outUser)),
					errLoad, strings.TrimSpace(string(outLoad)))
			}
		}
	}

	// 4. Verify that the service is actually registered in launchd domain
	if !isServiceRegistered(uid, label) {
		return fmt.Errorf("failed to enable LaunchAgent: service %q is not registered in launchd domain after bootstrap/load", label)
	}

	return nil
}

// Disable unloads and removes the LaunchAgent plist file. Unrelated plists are never touched.
func Disable() error {
	launchDir := getLaunchAgentsDir()
	plistPath := filepath.Join(launchDir, GetPlistFileName())

	uid := os.Getuid()
	label := GetServiceLabel()
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, label)).Run()
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("user/%d/%s", uid, label)).Run()
	_ = execCommand("launchctl", "unload", "-w", plistPath).Run()

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove LaunchAgent plist: %w", err)
	}
	return nil
}

// Restart reloads the daemon. If oldMachineID is provided and differs from the current configuration,
// it first publishes an offline status for oldMachineID to MQTT so it does not linger ONLINE.
func Restart(cfg *config.Config, oldMachineID string, exePath string) error {
	if cfg != nil && oldMachineID != "" && cfg.MachineID != oldMachineID {
		_ = PublishOffline(cfg.Broker, oldMachineID)
	}

	if err := Disable(); err != nil {
		return fmt.Errorf("failed to stop daemon: %w", err)
	}

	return Enable(cfg, exePath)
}

// Status inspects the current LaunchAgent configuration and execution state.
func Status() StatusInfo {
	plistPath := filepath.Join(getLaunchAgentsDir(), GetPlistFileName())
	installed := false
	if _, err := os.Stat(plistPath); err == nil {
		installed = true
	}

	running := false
	detail := "未安装"
	if installed {
		detail = "已安装 (未运行)"
		uid := os.Getuid()
		label := GetServiceLabel()
		out, err := execCommand("launchctl", "print", fmt.Sprintf("gui/%d/%s", uid, label)).CombinedOutput()
		if err == nil && (strings.Contains(string(out), "state = running") || strings.Contains(string(out), "pid = ")) {
			running = true
			detail = "LaunchAgent 运行中"
		} else {
			listOut, listErr := execCommand("launchctl", "list", label).CombinedOutput()
			if listErr == nil && !strings.Contains(string(listOut), "Could not find") {
				running = true
				detail = "LaunchAgent 运行中"
			}
		}
	}

	return StatusInfo{
		Supported: true,
		Installed: installed,
		Running:   running,
		Detail:    detail,
		PlistPath: plistPath,
	}
}

func isLaunchctlFailure(out []byte) bool {
	if len(out) == 0 {
		return false
	}
	s := strings.ToLower(string(out))
	return strings.Contains(s, "failed") ||
		strings.Contains(s, "input/output error") ||
		strings.Contains(s, "could not find") ||
		strings.Contains(s, "bad request") ||
		strings.Contains(s, "error:") ||
		strings.Contains(s, "not found")
}

func isServiceRegistered(uid int, label string) bool {
	// Try launchctl print gui/<uid>/<label>
	outPrintGui, errPrintGui := execCommand("launchctl", "print", fmt.Sprintf("gui/%d/%s", uid, label)).CombinedOutput()
	if errPrintGui == nil && !isLaunchctlFailure(outPrintGui) {
		return true
	}

	// Try launchctl print user/<uid>/<label>
	outPrintUser, errPrintUser := execCommand("launchctl", "print", fmt.Sprintf("user/%d/%s", uid, label)).CombinedOutput()
	if errPrintUser == nil && !isLaunchctlFailure(outPrintUser) {
		return true
	}

	// Try launchctl list <label>
	outList, errList := execCommand("launchctl", "list", label).CombinedOutput()
	if errList == nil && !strings.Contains(string(outList), "Could not find") && !isLaunchctlFailure(outList) {
		return true
	}

	return false
}

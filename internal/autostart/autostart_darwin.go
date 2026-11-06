//go:build darwin

package autostart

import (
	"bytes"
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
`, ServiceLabel, exePath, home, home, pathEnv, stdoutPath, stderrPath)
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

	plistPath := filepath.Join(launchDir, PlistFileName)
	content := GeneratePlistContent(bin, home, logsDir)

	if err := os.WriteFile(plistPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write LaunchAgent plist %q: %w", plistPath, err)
	}

	// Bootstrap service as current user
	uid := os.Getuid()

	// Best-effort bootout first to clean up any previous instance
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, ServiceLabel)).Run()
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("user/%d/%s", uid, ServiceLabel)).Run()

	// Try bootstrap in gui domain
	cmd := execCommand("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), plistPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		// Fall back to user domain (e.g. headless/ssh session)
		cmdUser := execCommand("launchctl", "bootstrap", fmt.Sprintf("user/%d", uid), plistPath)
		if errUser := cmdUser.Run(); errUser != nil {
			// Legacy fallback
			cmdLoad := execCommand("launchctl", "load", "-w", plistPath)
			if errLoad := cmdLoad.Run(); errLoad != nil {
				return fmt.Errorf("failed to enable LaunchAgent: all launchctl attempts failed (bootstrap gui: %v; bootstrap user: %v; load: %w)", err, errUser, errLoad)
			}
		}
	}

	return nil
}

// Disable unloads and removes the LaunchAgent plist file. Unrelated plists are never touched.
func Disable() error {
	launchDir := getLaunchAgentsDir()
	plistPath := filepath.Join(launchDir, PlistFileName)

	uid := os.Getuid()
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, ServiceLabel)).Run()
	_ = execCommand("launchctl", "bootout", fmt.Sprintf("user/%d/%s", uid, ServiceLabel)).Run()
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
	plistPath := filepath.Join(getLaunchAgentsDir(), PlistFileName)
	installed := false
	if _, err := os.Stat(plistPath); err == nil {
		installed = true
	}

	running := false
	detail := "未安装"
	if installed {
		detail = "已安装 (未运行)"
		uid := os.Getuid()
		out, err := execCommand("launchctl", "print", fmt.Sprintf("gui/%d/%s", uid, ServiceLabel)).CombinedOutput()
		if err == nil && (strings.Contains(string(out), "state = running") || strings.Contains(string(out), "pid = ")) {
			running = true
			detail = "LaunchAgent 运行中"
		} else {
			listOut, listErr := execCommand("launchctl", "list", ServiceLabel).CombinedOutput()
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

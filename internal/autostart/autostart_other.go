//go:build !darwin

package autostart

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

// SetTestOverrides is a no-op placeholder on non-darwin platforms.
func SetTestOverrides(launchAgentsDir, logsDir string, cmdFn func(name string, args ...string) *exec.Cmd) func() {
	return func() {}
}

// IsSupported reports whether login autostart is supported on the current operating system.
// On Linux/Windows, this is currently a placeholder returning an unsupported platform error.
func IsSupported() bool {
	return false
}

// Enable reports that autostart is not yet supported on this platform.
func Enable(cfg *config.Config, exePath string) error {
	return fmt.Errorf("开机自启常驻后台暂不支持当前操作系统 (%s)，目前仅支持 macOS (LaunchAgent)", runtime.GOOS)
}

// Disable is a no-op on platforms without LaunchAgent support.
func Disable() error {
	return nil
}

// Restart reports platform unsupported.
func Restart(cfg *config.Config, oldMachineID string, exePath string) error {
	if cfg != nil && oldMachineID != "" && cfg.MachineID != oldMachineID {
		_ = PublishOffline(cfg.Broker, oldMachineID)
	}
	return nil
}

// Status returns a status indicating lack of platform support.
func Status() StatusInfo {
	return StatusInfo{
		Supported: false,
		Installed: false,
		Running:   false,
		Detail:    fmt.Sprintf("当前平台 (%s) 暂未支持开机自启守护进程", runtime.GOOS),
	}
}

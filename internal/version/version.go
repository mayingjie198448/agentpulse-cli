package version

import "fmt"

var (
	// Version is the current release version of AgentPulse CLI.
	Version = "0.1.0-alpha"
	// GitCommit is set at build time via -ldflags.
	GitCommit = "dev"
	// BuildDate is set at build time via -ldflags.
	BuildDate = "2026-11-02"
)

// Info returns formatted version info.
func Info() string {
	return fmt.Sprintf("agentpulse %s (commit: %s, built: %s)", Version, GitCommit, BuildDate)
}

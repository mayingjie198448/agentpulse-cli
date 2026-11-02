package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	plainOutput bool
)

// NewRootCmd constructs the root command for agentpulse.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "agentpulse",
		Short: "AgentPulse: Cross-machine monitor CLI for agy and OpenCode",
		Long: `AgentPulse monitors autonomous AI agent execution (Google Antigravity / agy,
and OpenCode) running across multiple remote machines and workstations.

In this initial release, data is provided by an interactive, real-time mock
engine simulating multi-node agent orchestration and telemetry.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.PersistentFlags().BoolVar(&plainOutput, "plain", false, "Force plain text output (disables interactive TUI)")
	registerStoreFlags(rootCmd)

	rootCmd.AddCommand(newWatchCmd())
	rootCmd.AddCommand(newListCmd())
	rootCmd.AddCommand(newLogsCmd())
	rootCmd.AddCommand(newDoctorCmd())
	rootCmd.AddCommand(newVersionCmd())

	return rootCmd
}

// Execute runs the root command.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

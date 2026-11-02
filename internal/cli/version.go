package cli

import (
	"fmt"

	"github.com/mayingjie198448/agentpulse-cli/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the AgentPulse CLI version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Info())
			fmt.Println("Cross-machine monitor for Google Antigravity (agy) and OpenCode")
			fmt.Println("License: MIT")
		},
	}
}

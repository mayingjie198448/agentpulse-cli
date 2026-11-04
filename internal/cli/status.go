package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var (
		statusFile string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Display local agent and wrapper status snapshot",
		Long: `Reads and displays the latest local status snapshot written by 'agentpulse wrap'.
Snapshot defaults to .agentpulse/status.json or the path specified via --status-file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			if statusFile == "" {
				statusFile = wrapper.DefaultStatusFile
			}

			st, err := wrapper.ReadLocalStatus(statusFile)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintf(out, "No local status snapshot found at %s (run 'agentpulse wrap' to start a job)\n", statusFile)
					return nil
				}
				return fmt.Errorf("failed to read status from %s: %w", statusFile, err)
			}

			if jsonOutput {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}

			// Format human-readable output
			fmt.Fprintln(out, "AgentPulse Local Status:")
			fmt.Fprintln(out, "----------------------------------------")
			fmt.Fprintf(out, "Node ID:       %s\n", st.Machine.ID)
			fmt.Fprintf(out, "Node Name:     %s\n", st.Machine.Name)
			fmt.Fprintf(out, "Platform:      %s\n", st.Machine.Platform)
			if st.Machine.IP != "" {
				fmt.Fprintf(out, "Local IP:      %s\n", st.Machine.IP)
			}
			nodeState := "ONLINE"
			if !st.Machine.Online {
				nodeState = "OFFLINE"
			}
			fmt.Fprintf(out, "Node State:    %s (last heartbeat: %s)\n", nodeState, st.Machine.HeartbeatAgo())
			fmt.Fprintln(out, "----------------------------------------")
			fmt.Fprintf(out, "Active Job ID: %s\n", st.Job.ID)
			fmt.Fprintf(out, "Engine:        %s\n", st.Job.Engine)
			fmt.Fprintf(out, "Job Status:    %s\n", st.Job.Status)
			fmt.Fprintf(out, "Progress:      %.1f%%\n", st.Job.Progress)
			if st.Job.Title != "" {
				fmt.Fprintf(out, "Title:         %s\n", st.Job.Title)
			}
			if st.Job.Command != "" {
				fmt.Fprintf(out, "Command:       %s\n", st.Job.Command)
			}
			if st.Job.Cwd != "" {
				fmt.Fprintf(out, "Directory:     %s\n", st.Job.Cwd)
			}
			if st.Job.DurationStr != "" {
				fmt.Fprintf(out, "Duration:      %s\n", st.Job.DurationStr)
			}
			if st.Job.Error != "" {
				fmt.Fprintf(out, "Error:         %s\n", st.Job.Error)
			}
			if st.PID > 0 {
				fmt.Fprintf(out, "PID:           %d\n", st.PID)
			}
			fmt.Fprintf(out, "Snapshot File: %s (updated: %s)\n", statusFile, st.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
			fmt.Fprintln(out, "----------------------------------------")

			return nil
		},
	}

	cmd.Flags().StringVar(&statusFile, "status-file", "", "Path to local status snapshot (default: .agentpulse/status.json)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output status as JSON")

	return cmd
}

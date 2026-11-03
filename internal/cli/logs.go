package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var follow bool
	var tail int

	cmd := &cobra.Command{
		Use:   "logs <job-id>",
		Short: "Fetch or stream logs for a specific job",
		Long: `Displays historical logs for an autonomous agent job.
Use --follow (-f) to stream live log events in real time.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID := args[0]
			st, err := getStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()

			ctx := cmd.Context()

			// Verify job exists
			job, err := st.GetJob(ctx, jobID)
			if err != nil {
				jobs, _ := st.ListJobs(ctx)
				fmt.Fprintf(os.Stderr, "Error: %v\n\nAvailable job IDs:\n", err)
				for _, j := range jobs {
					fmt.Fprintf(os.Stderr, "  - %-14s (%s on %s)\n", j.ID, j.Engine, j.MachineID)
				}
				return fmt.Errorf("job not found: %s", jobID)
			}

			// Print historical logs
			entries, err := st.GetLogs(ctx, jobID, tail)
			if err != nil {
				return err
			}

			fmt.Printf("=== Showing logs for %s (%s on %s) ===\n", job.ID, job.Engine, job.MachineID)
			for _, e := range entries {
				printLogLine(e)
			}

			if !follow {
				return nil
			}

			fmt.Println("--- Streaming live logs (Ctrl+C to stop) ---")
			eventCh, err := st.Subscribe(ctx)
			if err != nil {
				return err
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

			for {
				select {
				case <-ctx.Done():
					return nil
				case <-sigCh:
					return nil
				case ev, ok := <-eventCh:
					if !ok {
						return nil
					}
					if ev.Type == model.EventLogAdded && ev.Log != nil && ev.Log.JobID == jobID {
						printLogLine(*ev.Log)
					}
				}
			}
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream live logs continuously")
	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "Number of recent log lines to display")

	return cmd
}

func printLogLine(e model.LogEntry) {
	fmt.Printf("%s [%-5s] %s\n",
		e.Timestamp.Format("2006-01-02 15:04:05"),
		e.Level,
		e.Message,
	)
}

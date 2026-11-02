package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
	"github.com/mayingjie198448/agentpulse-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newWatchCmd() *cobra.Command {
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Launch real-time cross-machine monitor dashboard",
		Long: `Launches an interactive cyber-themed terminal dashboard (TUI) displaying
machines, live jobs (agy & OpenCode), progress bars, and scrolling logs.

When running in non-TTY environments (or when passing --plain), watch outputs
a plain text stream of status updates suitable for piping and logging.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()

			if termutil.ShouldUsePlain(plainOutput) {
				return runPlainWatch(cmd.Context(), st, interval)
			}

			// Interactive TUI mode with Bubble Tea
			p := tea.NewProgram(tui.NewModel(st), tea.WithAltScreen(), tea.WithMouseCellMotion())
			_, err = p.Run()
			return err
		},
	}

	cmd.Flags().DurationVarP(&interval, "interval", "i", 2*time.Second, "Update interval for plain text stream")
	return cmd
}

// runPlainWatch prints periodic status snapshots in non-interactive/piped environments
func runPlainWatch(ctx context.Context, st store.Store, interval time.Duration) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	printSnapshot := func() {
		machines, _ := st.ListMachines(ctx)
		jobs, _ := st.ListJobs(ctx)

		fmt.Printf("=== [AgentPulse Watch Snapshot @ %s] ===\n", time.Now().Format("15:04:05"))
		fmt.Printf("Machines (%d online / %d total):\n", countOnline(machines), len(machines))
		for _, m := range machines {
			status := "OFFLINE"
			if m.Online {
				status = "ONLINE"
			}
			fmt.Printf("  - %-12s [%-7s] heartbeat: %-8s jobs: %d\n", m.Name, status, m.HeartbeatAgo(), m.JobCount)
		}

		fmt.Printf("Jobs (%d total):\n", len(jobs))
		for _, j := range jobs {
			fmt.Printf("  - %-14s [%-8s] @%-10s [%-7s] %5.1f%% : %s\n",
				j.ID, j.Engine, j.MachineID, j.Status, j.Progress, j.Title)
		}
		fmt.Println()
	}

	// Print first snapshot immediately
	printSnapshot()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			fmt.Println("\nExiting watch...")
			return nil
		case <-ticker.C:
			printSnapshot()
		}
	}
}

func countOnline(machines []model.Machine) int {
	cnt := 0
	for _, m := range machines {
		if m.Online {
			cnt++
		}
	}
	return cnt
}

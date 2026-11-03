package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all monitored machines and agent jobs",
		Long:  `Outputs a summary table of registered machines and current agent jobs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := getStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()

			ctx := cmd.Context()
			machines, err := st.ListMachines(ctx)
			if err != nil {
				return err
			}
			jobs, err := st.ListJobs(ctx)
			if err != nil {
				return err
			}

			if jsonOutput {
				payload := struct {
					Machines []model.Machine `json:"machines"`
					Jobs     []model.Job     `json:"jobs"`
				}{
					Machines: machines,
					Jobs:     jobs,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}

			// Format machine table
			fmt.Println("MACHINES:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATUS\tHEARTBEAT\tPLATFORM\tIP\tACTIVE JOBS")
			for _, m := range machines {
				status := "ONLINE"
				if !m.Online {
					status = "OFFLINE"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n",
					m.Name, status, m.HeartbeatAgo(), m.Platform, m.IP, m.JobCount)
			}
			w.Flush()

			fmt.Println("\nJOBS:")
			wJobs := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(wJobs, "JOB ID\tMACHINE\tENGINE\tSTATUS\tPROGRESS\tTITLE\tCWD")
			for _, j := range jobs {
				fmt.Fprintf(wJobs, "%s\t%s\t%s\t%s\t%5.1f%%\t%s\t%s\n",
					j.ID, j.MachineID, j.Engine, j.Status, j.Progress, j.Title, j.Cwd)
			}
			wJobs.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output list in JSON format")
	return cmd
}

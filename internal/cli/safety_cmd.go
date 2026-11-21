package cli

import (
	"fmt"

	"github.com/mayingjie198448/agentpulse-cli/internal/safety"
	"github.com/spf13/cobra"
)

// newSafetyCmd is the ops cleanup entry. It deletes by recorded run id only.
// There is no path flag and no HOME-based recursive delete.
func newSafetyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "safety",
		Short:  "Fail-closed isolation status and owned-root cleanup",
		Hidden: true,
	}
	var runID, registry, audit, realHome string
	cleanup := &cobra.Command{
		Use:   "cleanup",
		Short: "Delete one run root identified by its creation record",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runID == "" {
				return fmt.Errorf("cleanup requires --run-id from this run's ownership record")
			}
			out, err := safety.CleanupRun(runID, safety.CleanupOptions{
				RegistryDir: registry,
				AuditPath:   audit,
				RealHome:    realHome,
			})
			fmt.Fprintf(cmd.OutOrStdout(), "cleanup_result=%s\n", out.Result)
			if out.Reason != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "cleanup_reason=%s\n", out.Reason)
			}
			return err
		},
	}
	cleanup.Flags().StringVar(&runID, "run-id", "", "run id recorded at creation")
	cleanup.Flags().StringVar(&registry, "registry", "", "ownership registry directory (not a delete target)")
	cleanup.Flags().StringVar(&audit, "audit", "", "audit log path outside the run root")
	cleanup.Flags().StringVar(&realHome, "protect-home", "", "additional home path that must not be deleted")
	cmd.AddCommand(cleanup)

	backend := &cobra.Command{
		Use:   "backend",
		Short: "Show the write-isolation backend and BLOCKED surfaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			b, err := safety.SelectBackend()
			name := "unavailable"
			if b != nil {
				name = b.Name()
			}
			fmt.Fprintf(out, "backend=%s\n", name)
			for k, v := range safety.NativeVerification() {
				fmt.Fprintf(out, "%s=%s\n", k, v)
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "backend_error=%s\n", err.Error())
				return err
			}
			return nil
		},
	}
	cmd.AddCommand(backend)

	fixture := &cobra.Command{
		Use:   "fixture",
		Short: "Run the native confinement fixture against synthetic files only. Does not launch agy.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := safety.RunNativeFixture(cmd.Context())
			fmt.Fprint(cmd.OutOrStdout(), report)
			return err
		},
	}
	cmd.AddCommand(fixture)
	return cmd
}

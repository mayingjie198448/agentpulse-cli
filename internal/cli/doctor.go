package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Perform diagnostic checks on terminal and engine adapters",
		Long: `Validates local terminal environment, CLI capabilities, and checks
availability of engine CLI binaries (agy, opencode) and future network adapters.`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("AgentPulse Diagnostics:")
			fmt.Println("----------------------------------------")

			// 1. Runtime & OS
			fmt.Printf("[OK] Runtime: Go %s on %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

			// 2. Terminal
			isTTY := termutil.IsTerminal(os.Stdout)
			if isTTY {
				w, h, err := term.GetSize(int(os.Stdout.Fd()))
				if err == nil {
					fmt.Printf("[OK] Terminal: Interactive TTY detected (Dimensions: %dx%d)\n", w, h)
				} else {
					fmt.Printf("[OK] Terminal: Interactive TTY detected\n")
				}
			} else {
				fmt.Println("[INFO] Terminal: Non-interactive TTY / pipe (will fallback to plain text)")
			}

			// 3. Engine CLI availability
			if p, err := exec.LookPath("agy"); err == nil {
				fmt.Printf("[OK] Engine 'agy': Detected at %s\n", p)
			} else {
				fmt.Println("[INFO] Engine 'agy': Not found in PATH (Local jobs will use simulated adapter)")
			}

			if p, err := exec.LookPath("opencode"); err == nil {
				fmt.Printf("[OK] Engine 'OpenCode': Detected at %s\n", p)
			} else {
				fmt.Println("[INFO] Engine 'OpenCode': Not found in PATH (Local jobs will use simulated adapter)")
			}

			// 4. Data & Network Adapters
			fmt.Println("[OK] Data Layer: In-memory simulation store active")
			fmt.Println("[INFO] MQTT Transport: Stubbed for Phase 2 (Cross-machine broker adapter)")
			fmt.Println("[INFO] Sidecar Agent: Stubbed for Phase 2 (Node telemetry daemon)")

			fmt.Println("----------------------------------------")
			fmt.Println("Status: All systems ready for Phase 1 Mock TUI.")
		},
	}
}

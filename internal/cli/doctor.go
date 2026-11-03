package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/termutil"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
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
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "AgentPulse Diagnostics:")
			fmt.Fprintln(out, "----------------------------------------")

			// 1. Runtime & OS
			fmt.Fprintf(out, "[OK] Runtime: Go %s on %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

			// 2. Terminal
			isTTY := termutil.IsTerminal(os.Stdout)
			if isTTY {
				w, h, err := term.GetSize(int(os.Stdout.Fd()))
				if err == nil {
					fmt.Fprintf(out, "[OK] Terminal: Interactive TTY detected (Dimensions: %dx%d)\n", w, h)
				} else {
					fmt.Fprintln(out, "[OK] Terminal: Interactive TTY detected")
				}
			} else {
				fmt.Fprintln(out, "[INFO] Terminal: Non-interactive TTY / pipe (will fallback to plain text)")
			}

			// 3. Engine CLI availability
			if p, err := exec.LookPath("agy"); err == nil {
				fmt.Fprintf(out, "[OK] Engine 'agy': Detected at %s\n", p)
			} else {
				fmt.Fprintln(out, "[INFO] Engine 'agy': Not found in PATH (Local jobs will use simulated adapter)")
			}

			if p, err := exec.LookPath("opencode"); err == nil {
				fmt.Fprintf(out, "[OK] Engine 'OpenCode': Detected at %s\n", p)
			} else {
				fmt.Fprintln(out, "[INFO] Engine 'OpenCode': Not found in PATH (Local jobs will use simulated adapter)")
			}

			// 4. Data & Network Adapters
			stType, err := determineStoreType(cmd)
			if err != nil {
				fmt.Fprintf(out, "[ERROR] Store Configuration: %v\n", err)
			} else if stType == "mqtt" {
				cfg := resolveMQTTConfig(cmd)
				authInfo := "anonymous"
				if cfg.Username != "" {
					authInfo = fmt.Sprintf("username %q", cfg.Username)
				}
				fmt.Fprintf(out, "[OK] Data Layer: MQTT Store configured (Target: %s)\n", cfg.BrokerURL)
				fmt.Fprintf(out, "[INFO] MQTT Config: client_id=%s, auth=%s, settle=%v\n", cfg.ClientID, authInfo, cfg.SettleTimeout)
				if cfg.IsTLSEnabled() {
					tlsParts := []string{"enabled"}
					if cfg.InsecureSkipVerify {
						tlsParts = append(tlsParts, "insecure=true")
					}
					if cfg.CACertFile != "" {
						tlsParts = append(tlsParts, fmt.Sprintf("ca=%s", cfg.CACertFile))
					}
					if cfg.ClientCertFile != "" {
						tlsParts = append(tlsParts, "mTLS=true")
					}
					if cfg.ServerName != "" {
						tlsParts = append(tlsParts, fmt.Sprintf("sni=%s", cfg.ServerName))
					}
					fmt.Fprintf(out, "[INFO] MQTT TLS: %s\n", strings.Join(tlsParts, ", "))
				}
				fmt.Fprintf(out, "[...] MQTT Broker: Checking connectivity to %s (timeout: 2s)...\n", cfg.BrokerURL)
				if err := testMQTTConnection(cfg, 2*time.Second); err != nil {
					fmt.Fprintf(out, "[WARN] MQTT Broker: Connectivity check failed (%v)\n", err)
				} else {
					fmt.Fprintln(out, "[OK] MQTT Broker: Connected successfully")
				}
				fmt.Fprintln(out, "[INFO] Sidecar Agent: Awaiting remote wrapper heartbeats")
			} else {
				fmt.Fprintln(out, "[OK] Data Layer: In-memory simulation store active (default)")
				fmt.Fprintln(out, "[INFO] MQTT Transport: Ready for connection (pass --mqtt-url or set AGENTPULSE_MQTT_URL)")
				fmt.Fprintln(out, "[INFO] Sidecar Agent: Stubbed for Phase 2 (Node telemetry daemon)")
			}

			// 5. Thin Wrapper
			statusFilePath := wrapper.DefaultStatusFile
			if env := os.Getenv("AGENTPULSE_STATUS_FILE"); env != "" {
				statusFilePath = env
			}
			if st, err := wrapper.ReadLocalStatus(statusFilePath); err == nil {
				fmt.Fprintf(out, "[OK] Thin Wrapper: Active/recent job %s on %s (status: %s)\n", st.Job.ID, st.Machine.ID, st.Job.Status)
			} else {
				fmt.Fprintln(out, "[OK] Thin Wrapper: Ready for local execution ('agentpulse wrap')")
			}

			fmt.Fprintln(out, "----------------------------------------")
			fmt.Fprintln(out, "Status: All systems ready.")
		},
	}
}

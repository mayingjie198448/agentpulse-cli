package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

func newWrapCmd() *cobra.Command {
	var (
		machineFlag                string
		machineID                  string
		machineName                string
		jobID                      string
		engine                     string
		title                      string
		statusFile                 string
		heartbeatInterval          time.Duration
		stub                       bool
		promptFlag                 string
		dangerouslySkipPermissions bool
		quiet                      bool
		listen                     bool
		maxConcurrentJobs          int
	)

	wrapCmd := &cobra.Command{
		Use:   "wrap [flags] [--] [command] [args...]",
		Short: "Wrap and monitor a local agent process (agy / opencode) with MQTT telemetry",
		Long: `Executes an AI agent command locally on this machine while capturing stdout/stderr
logs, writing local status snapshots, and publishing heartbeat, job lifecycle, and log
telemetry to an MQTT broker per docs/mqtt-protocol.md.

If --mqtt-url or broker.url in config.yaml is configured, telemetry is streamed in real-time.
Otherwise, the wrapper operates in local mode without requiring a broker.

Node identity resolution precedence:
  1. Explicit CLI flag (--machine / --machine-id)
  2. Sanitized local hostname (lowercase alphanumeric with hyphens)

Examples:
  # Wrap agy with auto tool approval:
  agentpulse wrap -- agy --dangerously-skip-permissions -p "Fix bug in auth.go"

  # Wrap agy via flag shorthand:
  agentpulse wrap --dangerously-skip-permissions agy -p "Refactor store"

  # Stream telemetry to an MQTT broker:
  agentpulse wrap --mqtt-url tcp://broker.example:1883 -- agy -p "Run tests"

  # Run simulated stub execution (e.g. for OpenCode or test automation):
  agentpulse wrap --engine opencode --stub`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, rawArgs []string) error {
			out := cmd.OutOrStdout()
			errOut := cmd.OutOrStderr()

			if machineID == "" && machineFlag != "" {
				machineID = machineFlag
			}

			if listen {
				return runListen(cmd, machineID, machineName, statusFile, heartbeatInterval, stub, dangerouslySkipPermissions, quiet, maxConcurrentJobs)
			}

			// 状态文件只来自 --status-file，否则使用默认路径。
			if statusFile == "" {
				statusFile = wrapper.DefaultStatusFile
			}

			// Resolve engine
			var engType model.EngineType
			switch strings.ToLower(engine) {
			case "opencode":
				engType = model.EngineOpenCode
			case "agy":
				engType = model.EngineAgy
			default:
				if engine != "" {
					return fmt.Errorf("invalid engine %q: must be 'agy' or 'opencode'", engine)
				}
			}

			// Process arguments
			args := make([]string, len(rawArgs))
			copy(args, rawArgs)

			// If no command specified but prompt flag passed
			if len(args) == 0 && promptFlag != "" {
				args = []string{"agy", "-p", promptFlag}
				if dangerouslySkipPermissions {
					args = []string{"agy", "--dangerously-skip-permissions", "-p", promptFlag}
				}
			}

			// Handle --dangerously-skip-permissions for agy commands
			if dangerouslySkipPermissions && len(args) > 0 {
				base := filepath.Base(args[0])
				if base == "agy" {
					hasPermFlag := false
					for _, a := range args {
						if a == "--dangerously-skip-permissions" {
							hasPermFlag = true
							break
						}
					}
					if !hasPermFlag {
						args = append([]string{args[0], "--dangerously-skip-permissions"}, args[1:]...)
					}
				}
			}

			if len(args) == 0 && !stub {
				return fmt.Errorf("a command to wrap is required (e.g. agentpulse wrap -- agy --dangerously-skip-permissions -p \"...\")")
			}

			// Setup cancellation on SIGINT / SIGTERM
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			go func() {
				select {
				case <-sigCh:
					cancel()
				case <-ctx.Done():
				}
			}()

			// Resolve MQTT configuration
			mqttCfg := resolveMQTTConfig(cmd)
			var pMQTT *mqtt.Config
			if mqttCfg.BrokerURL != "" {
				pMQTT = &mqttCfg
			}

			w, err := wrapper.New(wrapper.Config{
				MachineID:         machineID,
				MachineName:       machineName,
				JobID:             jobID,
				Engine:            engType,
				Title:             title,
				Args:              args,
				StatusFile:        statusFile,
				HeartbeatInterval: heartbeatInterval,
				MQTT:              pMQTT,
				MirrorStdout:      os.Stdout,
				MirrorStderr:      os.Stderr,
				Stub:              stub,
				Quiet:             quiet,
				OnMQTTConnected: func() {
					if !quiet && pMQTT != nil {
						fmt.Fprintf(out, "[agentpulse] Connected to MQTT broker %s (telemetry & logs active)\n", pMQTT.BrokerURL)
					}
				},
				OnExecute: func(cmdStr string) {
					if !quiet && cmdStr != "" {
						fmt.Fprintf(out, "[agentpulse] Executing: %s\n\n", cmdStr)
					}
				},
			})
			if err != nil {
				return fmt.Errorf("failed to initialize wrapper: %w", err)
			}

			if !quiet {
				fmt.Fprintf(out, "[agentpulse] Initialized job %s on node %s\n", w.Job().ID, w.Machine().ID)
				fmt.Fprintf(out, "[agentpulse] Local status snapshot: %s\n", statusFile)
				if pMQTT != nil {
					fmt.Fprintf(out, "[agentpulse] Connecting to MQTT broker %s...\n", pMQTT.BrokerURL)
				} else {
					fmt.Fprintln(out, "[agentpulse] Running in local mode (no MQTT broker configured)")
				}
			}

			runErr := w.Run(ctx)

			if !quiet {
				if runErr == nil {
					fmt.Fprintf(out, "\n[agentpulse] Job completed successfully in %s (status: done)\n", w.Job().DurationStr)
				} else {
					fmt.Fprintf(errOut, "\n[agentpulse] Job failed in %s: %v\n", w.Job().DurationStr, runErr)
				}
			}

			return runErr
		},
	}

	// Disable interspersed flag parsing so flags for wrapped binary are preserved as positional arguments
	wrapCmd.Flags().SetInterspersed(false)

	wrapCmd.Flags().StringVar(&machineFlag, "machine", "", "Machine ID override (default: sanitized hostname)")
	wrapCmd.Flags().StringVar(&machineID, "machine-id", "", "Machine ID override (alias for --machine)")
	wrapCmd.Flags().StringVar(&machineName, "machine-name", "", "Machine display name override (default: machine ID)")
	wrapCmd.Flags().StringVar(&jobID, "job-id", "", "Job ID override (default: generated)")
	wrapCmd.Flags().StringVar(&engine, "engine", "", "Agent engine type ('agy' or 'opencode')")
	wrapCmd.Flags().StringVar(&title, "title", "", "Job title or summary description")
	wrapCmd.Flags().StringVar(&statusFile, "status-file", "", "Path to write local status snapshot (default: .agentpulse/status.json)")
	wrapCmd.Flags().DurationVar(&heartbeatInterval, "heartbeat-interval", wrapper.DefaultHeartbeatInterval, "Interval between MQTT heartbeats")
	wrapCmd.Flags().BoolVar(&stub, "stub", false, "Run in simulated stub mode (useful for testing or when engine is uninstalled)")
	wrapCmd.Flags().StringVarP(&promptFlag, "prompt", "p", "", "Instruction/prompt for agent invocation")
	wrapCmd.Flags().BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", false, "Auto-approve tool permissions when wrapping agy")
	wrapCmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Suppress agentpulse wrapper header/footer banners")
	wrapCmd.Flags().BoolVar(&listen, "listen", false, "Run thin wrapper in listen mode to receive and execute remote dispatch commands")
	wrapCmd.Flags().IntVarP(&maxConcurrentJobs, "max-concurrent-jobs", "c", 0, "最大本地并发作业数 (用于 --listen 模式，默认 1)")

	return wrapCmd
}

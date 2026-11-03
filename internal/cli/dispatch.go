package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mayingjie198448/agentpulse-cli/internal/dispatch"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

func newDispatchCmd() *cobra.Command {
	var (
		machineFlag    string
		machineIDFlag  string
		jobID          string
		engine         string
		cwd            string
		title              string
		timeoutSeconds     int
		waitTimeoutSeconds int
		requestedBy        string
		envFlags       []string
		commandFlag    string
		argFlags       []string
		waitFlag       bool
		followFlag     bool
		quiet          bool
		forceFlag      bool
	)

	dispatchCmd := &cobra.Command{
		Use:   "dispatch [flags] [--] [command] [args...]",
		Short: "Dispatch an autonomous agent task to a remote machine over MQTT",
		Long: `Publishes a job dispatch instruction to agentpulse/v1/machines/{machine_id}/commands/dispatch
over an MQTT broker per docs/mqtt-protocol.md §4.4.

Target Machine Gate:
  By default in MQTT mode, dispatch queries the broker to verify that the target machine
  is known (has registered heartbeat) and currently online. If the machine is unknown or
  offline, dispatch fails immediately to prevent lost commands. Use --force to bypass
  this gate.

Commands and arguments can be specified either via trailing arguments after -- or via
--command and repeated --arg flags.

With --wait, the CLI waits until the dispatched job reaches done or failed status.
With --follow, the CLI additionally streams real-time stdout/stderr log events until completion.

Examples:
  # Dispatch agy command to remote machine:
  agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio -- agy run --prompt "Fix bug"

  # Force dispatch to a node that has not yet reported online:
  agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio --force -- agy -p "Bootstrap"

  # Dispatch via --command and repeated --arg:
  agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine linux-gpu --command opencode --arg run --arg "--prompt=Train model"

  # Dispatch, wait for completion, and follow live logs:
  agentpulse dispatch --mqtt-url tcp://broker.example:1883 --machine mac-studio --follow -- agy -p "Run test suite"`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, rawArgs []string) error {
			out := cmd.OutOrStdout()
			errOut := cmd.OutOrStderr()

			machineID := machineFlag
			if machineID == "" {
				machineID = machineIDFlag
			}
			if machineID == "" {
				return fmt.Errorf("machine ID is required (pass --machine or --machine-id)")
			}
			machineID = wrapper.SanitizeID(machineID)

			// Resolve MQTT configuration
			mqttCfg := resolveMQTTConfig(cmd)
			if mqttCfg.BrokerURL == "" {
				return fmt.Errorf("MQTT broker URL is required for dispatch (pass --mqtt-url or set AGENTPULSE_MQTT_URL)")
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

			// Parse environment variables
			envMap := make(map[string]string)
			for _, e := range envFlags {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				} else {
					envMap[parts[0]] = ""
				}
			}

			// Resolve command & args
			var finalCmd string = commandFlag
			var finalArgs []string

			if len(rawArgs) > 0 {
				if finalCmd == "" {
					finalCmd = rawArgs[0]
					finalArgs = append(finalArgs, rawArgs[1:]...)
				} else {
					finalArgs = append(finalArgs, rawArgs...)
				}
			}
			if len(argFlags) > 0 {
				finalArgs = append(finalArgs, argFlags...)
			}

			if strings.TrimSpace(finalCmd) == "" && len(finalArgs) == 0 {
				return fmt.Errorf("a command to dispatch is required (e.g. via -- command [args...] or --command <cmd>)")
			}

			// Setup cancellation
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

			pMQTT := &mqttCfg

			cfg := dispatch.Config{
				MachineID:          machineID,
				JobID:              jobID,
				Engine:             engType,
				Cwd:                cwd,
				Command:            finalCmd,
				Args:               finalArgs,
				Title:              title,
				TimeoutSeconds:     timeoutSeconds,
				WaitTimeoutSeconds: waitTimeoutSeconds,
				Env:                envMap,
				RequestedBy:        requestedBy,
				Wait:               waitFlag,
				Follow:             followFlag,
				Quiet:              quiet,
				Force:              forceFlag,
				MQTT:               pMQTT,
				Out:                out,
				ErrOut:             errOut,
			}

			_, err := dispatch.Dispatch(ctx, cfg)
			return err
		},
	}

	dispatchCmd.Flags().SetInterspersed(false)

	dispatchCmd.Flags().StringVar(&machineFlag, "machine", "", "Target machine ID")
	dispatchCmd.Flags().StringVar(&machineIDFlag, "machine-id", "", "Target machine ID (alias for --machine)")
	dispatchCmd.Flags().StringVar(&jobID, "job-id", "", "Job ID override (auto-generated if omitted)")
	dispatchCmd.Flags().StringVar(&engine, "engine", "", "Agent engine type ('agy' or 'opencode')")
	dispatchCmd.Flags().StringVar(&cwd, "cwd", "", "Remote target working directory")
	dispatchCmd.Flags().StringVar(&title, "title", "", "Job title or summary description")
	dispatchCmd.Flags().IntVar(&timeoutSeconds, "timeout-seconds", 0, "Remote job execution timeout in seconds (0 for no limit)")
	dispatchCmd.Flags().IntVar(&waitTimeoutSeconds, "wait-timeout", 0, "Local wait wall-clock timeout in seconds (0 for no limit)")
	dispatchCmd.Flags().StringVar(&requestedBy, "requested-by", "", "Dispatcher identity tag (default: $USER@hostname)")
	dispatchCmd.Flags().StringSliceVarP(&envFlags, "env", "e", nil, "Environment variables for remote job (key=value, repeatable)")
	dispatchCmd.Flags().StringVar(&commandFlag, "command", "", "Executable command to invoke")
	dispatchCmd.Flags().StringSliceVar(&argFlags, "arg", nil, "Command arguments (repeatable)")
	dispatchCmd.Flags().BoolVar(&waitFlag, "wait", false, "Wait for dispatched job completion and report progress")
	dispatchCmd.Flags().BoolVar(&followFlag, "follow", false, "Wait for dispatched job completion and stream real-time logs")
	dispatchCmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Suppress banner and status transition outputs")
	dispatchCmd.Flags().BoolVar(&forceFlag, "force", false, "Force dispatch even if target machine is unknown or offline")

	return dispatchCmd
}

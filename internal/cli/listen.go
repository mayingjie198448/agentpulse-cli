package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
	"github.com/spf13/cobra"
)

func runListen(cmd *cobra.Command, machineID, machineName, statusFile string, heartbeatInterval time.Duration, stub, dangerouslySkipPermissions, quiet bool) error {
	out := cmd.OutOrStdout()
	errOut := cmd.OutOrStderr()

	mqttCfg := resolveMQTTConfig(cmd)
	if mqttCfg.BrokerURL == "" {
		return fmt.Errorf("MQTT broker URL is required for listen mode (pass --mqtt-url or set broker.url in config.yaml)")
	}

	if statusFile == "" {
		statusFile = wrapper.DefaultStatusFile
	}

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

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:                  machineID,
		MachineName:                machineName,
		HeartbeatInterval:          heartbeatInterval,
		StatusFile:                 statusFile,
		MQTT:                       pMQTT,
		Stub:                       stub,
		DangerouslySkipPermissions: dangerouslySkipPermissions,
		Quiet:                      quiet,
		Stdout:                     out,
		Stderr:                     errOut,
		OnMQTTConnected: func() {
			if !quiet {
				fmt.Fprintf(out, "[agentpulse] Connected to MQTT broker %s\n", pMQTT.BrokerURL)
			}
		},
		OnListening: func(topic string) {
			if !quiet {
				fmt.Fprintf(out, "[agentpulse listener] Listening on %s (QoS 1, serial queue)\n", topic)
				fmt.Fprintln(out, "[agentpulse listener] Waiting for incoming dispatch commands... (Press Ctrl+C to stop)")
			}
		},
	})
	if err != nil {
		return fmt.Errorf("failed to initialize listener: %w", err)
	}

	if !quiet {
		fmt.Fprintf(out, "[agentpulse listener] Initializing node %s\n", l.Machine().ID)
		fmt.Fprintf(out, "[agentpulse] Connecting to MQTT broker %s...\n", pMQTT.BrokerURL)
	}

	runErr := l.Run(ctx)
	if runErr != nil && runErr != context.Canceled {
		return runErr
	}

	if !quiet {
		fmt.Fprintln(out, "\n[agentpulse listener] Stopped listening cleanly (offline status published)")
	}
	return nil
}

func newListenCmd() *cobra.Command {
	var (
		machineFlag                string
		machineIDFlag              string
		machineName                string
		statusFile                 string
		heartbeatInterval          time.Duration
		stub                       bool
		dangerouslySkipPermissions bool
		quiet                      bool
	)

	listenCmd := &cobra.Command{
		Use:   "listen [flags]",
		Short: "Run thin wrapper daemon in listen mode to receive and execute remote dispatch commands",
		Long: `Runs an edge listener process that connects to an MQTT broker, maintains
node heartbeat and LWT, subscribes to agentpulse/v1/machines/{machine_id}/commands/dispatch,
and serially executes incoming AI agent jobs (agy / opencode).

Node identity resolution precedence:
  1. Explicit CLI flag (--machine / --machine-id)
  2. Sanitized local hostname (lowercase alphanumeric with hyphens)

Examples:
  # Listen for remote dispatch on broker:
  agentpulse listen --mqtt-url tcp://broker.example:1883

  # Listen with custom machine ID and stub execution mode:
  agentpulse listen --mqtt-url tcp://broker.example:1883 --machine mac-studio --stub`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			machineID := machineFlag
			if machineID == "" {
				machineID = machineIDFlag
			}
			return runListen(cmd, machineID, machineName, statusFile, heartbeatInterval, stub, dangerouslySkipPermissions, quiet)
		},
	}

	listenCmd.Flags().StringVar(&machineFlag, "machine", "", "Machine ID override (default: sanitized hostname)")
	listenCmd.Flags().StringVar(&machineIDFlag, "machine-id", "", "Machine ID override (alias for --machine)")
	listenCmd.Flags().StringVar(&machineName, "machine-name", "", "Machine display name override (default: machine ID)")
	listenCmd.Flags().StringVar(&statusFile, "status-file", "", "Path to write local status snapshot (default: .agentpulse/status.json)")
	listenCmd.Flags().DurationVar(&heartbeatInterval, "heartbeat-interval", wrapper.DefaultHeartbeatInterval, "Interval between MQTT heartbeats")
	listenCmd.Flags().BoolVar(&stub, "stub", false, "Run executed jobs in simulated stub mode")
	listenCmd.Flags().BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", false, "Auto-approve tool permissions when running agy commands")
	listenCmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Suppress agentpulse listener header/footer banners")

	return listenCmd
}

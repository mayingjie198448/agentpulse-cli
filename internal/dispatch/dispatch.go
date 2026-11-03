package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

// Config configures a job dispatch invocation.
type Config struct {
	MachineID          string
	AgentName          string // Requested agent CLI or alias (0.5.0 gate check)
	JobID              string
	CommandID          string
	Engine             model.EngineType
	Cwd                string
	Command            string
	Args               []string
	Title              string
	TimeoutSeconds     int // Constrains remote job execution timeout (passed in payload)
	WaitTimeoutSeconds int // Local wait wall-clock timeout in seconds (0 = unlimited)
	Env                map[string]string
	RequestedBy        string

	Wait   bool
	Follow bool
	Quiet  bool
	Force  bool // Bypass target machine online/availability gate

	Store      store.Store // Optional store to check target machine availability before dispatch
	MQTT       *mqtt.Config
	MQTTClient paho.Client

	Out    io.Writer
	ErrOut io.Writer

	// Test hooks
	OnConnecting func(broker string)
	OnConnected  func(broker string)
	OnSubscribed func()
	OnDispatched func(cmd *model.DispatchCommand)
	OnProgress   func(job model.Job)
	OnLog        func(entry model.LogEntry)
}

// Dispatch validates arguments, connects to MQTT, publishes a dispatch command
// to agentpulse/v1/machines/{machine_id}/commands/dispatch per docs/mqtt-protocol.md §4.4,
// and optionally waits for or follows job completion telemetry.
func Dispatch(ctx context.Context, cfg Config) (*model.DispatchCommand, error) {
	if strings.TrimSpace(cfg.MachineID) == "" {
		return nil, fmt.Errorf("machine ID is required (pass --machine or --machine-id)")
	}
	cfg.MachineID = wrapper.SanitizeID(cfg.MachineID)

	parts := wrapper.BuildCommandArgs(cfg.Command, cfg.Args)
	if len(parts) == 0 {
		return nil, fmt.Errorf("a command to dispatch is required (e.g. via -- command [args...] or --command <cmd>)")
	}

	commandBin := parts[0]
	commandArgs := parts[1:]

	if cfg.Engine == "" {
		cfg.Engine = wrapper.DetectEngine(parts)
	}
	if cfg.JobID == "" {
		cfg.JobID = wrapper.GenerateJobID(cfg.Engine)
	}
	if cfg.CommandID == "" {
		cfg.CommandID = wrapper.GenerateCommandID()
	}
	if cfg.Title == "" {
		cfg.Title = wrapper.InferTitle(commandArgs, commandBin)
	}
	if cfg.RequestedBy == "" {
		user := os.Getenv("USER")
		if user == "" {
			user = "user"
		}
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "localhost"
		}
		cfg.RequestedBy = fmt.Sprintf("%s@%s", user, host)
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.ErrOut == nil {
		cfg.ErrOut = os.Stderr
	}

	var client paho.Client
	var ownsMQTT bool

	if cfg.MQTTClient != nil {
		client = cfg.MQTTClient
	} else {
		if cfg.MQTT == nil || cfg.MQTT.BrokerURL == "" {
			return nil, fmt.Errorf("MQTT broker URL is required for dispatch (pass --mqtt-url or set AGENTPULSE_MQTT_URL)")
		}

		if !cfg.Quiet {
			fmt.Fprintf(cfg.Out, "[agentpulse] Connecting to MQTT broker %s...\n", cfg.MQTT.BrokerURL)
		}
		if cfg.OnConnecting != nil {
			cfg.OnConnecting(cfg.MQTT.BrokerURL)
		}

		mqttCfg := *cfg.MQTT
		if mqttCfg.ClientID == "" {
			mqttCfg.ClientID = fmt.Sprintf("agentpulse-dispatch-%d", os.Getpid())
		}
		if mqttCfg.Timeout <= 0 {
			mqttCfg.Timeout = 5 * time.Second
		}

		opts := paho.NewClientOptions()
		if err := mqtt.ApplyClientOptions(opts, mqttCfg); err != nil {
			return nil, fmt.Errorf("MQTT client options error: %w", err)
		}
		opts.SetAutoReconnect(false)

		c := paho.NewClient(opts)
		token := c.Connect()
		if !token.WaitTimeout(mqttCfg.Timeout) {
			return nil, fmt.Errorf("MQTT connection timed out to %s", mqttCfg.BrokerURL)
		}
		if err := token.Error(); err != nil {
			return nil, fmt.Errorf("MQTT connection failed to %s: %w", mqttCfg.BrokerURL, err)
		}

		client = c
		ownsMQTT = true

		if !cfg.Quiet {
			fmt.Fprintf(cfg.Out, "[agentpulse] Connected to MQTT broker %s\n", mqttCfg.BrokerURL)
		}
		if cfg.OnConnected != nil {
			cfg.OnConnected(mqttCfg.BrokerURL)
		}
	}

	if ownsMQTT {
		defer client.Disconnect(100)
	}

	// Target machine gate per docs/mqtt-protocol.md §4.4 / multi-machine spec:
	// Verify target machine is known and online before dispatching unless --force is specified.
	if !cfg.Force {
		heartbeatTimeout := 45 * time.Second
		settleTimeout := 200 * time.Millisecond
		if cfg.MQTT != nil {
			if cfg.MQTT.HeartbeatTimeout > 0 {
				heartbeatTimeout = cfg.MQTT.HeartbeatTimeout
			}
			if cfg.MQTT.SettleTimeout > 0 {
				settleTimeout = cfg.MQTT.SettleTimeout
			}
		}

		if cfg.Store != nil {
			m, err := cfg.Store.GetMachine(ctx, cfg.MachineID)
			if err != nil || m == nil {
				return nil, fmt.Errorf("target machine %q is unknown (never registered or no heartbeat seen); ensure 'agentpulse' is active on target node, or use --force to bypass", cfg.MachineID)
			}
			if !m.Online || (!m.LastHeartbeat.IsZero() && time.Since(m.LastHeartbeat) > heartbeatTimeout) {
				return nil, fmt.Errorf("target machine %q is offline (last heartbeat %s); check node listen status and connectivity, or use --force to bypass", cfg.MachineID, m.HeartbeatAgo())
			}

			// 0.5.0 Agent readiness gate
			if cfg.AgentName != "" {
				ann, err := cfg.Store.GetAnnounce(ctx, cfg.MachineID)
				if err != nil || ann == nil {
					return nil, fmt.Errorf("target machine %q has no announce registered (cannot verify agent %q); check doctor or use --force to bypass", cfg.MachineID, cfg.AgentName)
				}
				ag, ok := ann.FindAgent(cfg.AgentName)
				if !ok || !ag.Ok {
					detail := "not announced"
					if ok {
						detail = ag.Detail
					}
					return nil, fmt.Errorf("target machine %q agent %q is not available (ok: false: %s); check doctor or use --force to bypass", cfg.MachineID, cfg.AgentName, detail)
				}
			}
		} else {
			// Query broker retained status and announce on the existing connected client without opening a second connection
			if err := verifyTargetMachineOnClient(ctx, client, cfg.MachineID, cfg.AgentName, settleTimeout, heartbeatTimeout); err != nil {
				return nil, err
			}
		}
	}

	dispatchStartTime := time.Now().UTC()

	// Setup wait / follow subscriptions BEFORE publishing dispatch command
	var statusCh chan model.Job
	var logCh chan model.LogEntry

	if cfg.Wait || cfg.Follow {
		statusCh = make(chan model.Job, 32)
		logCh = make(chan model.LogEntry, 256)

		statusTopic := mqtt.JobStatusTopic(cfg.MachineID, cfg.JobID)
		subToken := client.Subscribe(statusTopic, 1, func(_ paho.Client, msg paho.Message) {
			var j model.Job
			if err := json.Unmarshal(msg.Payload(), &j); err != nil || j.ID != cfg.JobID {
				return
			}

			// C1: Ignore stale retained status from an earlier run before this dispatch
			ts := j.UpdatedAt
			if ts.IsZero() {
				ts = j.StartedAt
			}
			if !ts.IsZero() && ts.Before(dispatchStartTime) {
				return
			}

			// Important #2: Deliver to statusCh keeping latest and never silently dropping terminal states
			for {
				select {
				case statusCh <- j:
					return
				case <-ctx.Done():
					return
				default:
					select {
					case <-statusCh:
					default:
					}
				}
			}
		})
		if !subToken.WaitTimeout(5 * time.Second) {
			return nil, fmt.Errorf("subscription timed out for %s", statusTopic)
		}
		if err := subToken.Error(); err != nil {
			return nil, fmt.Errorf("subscription failed for %s: %w", statusTopic, err)
		}

		if cfg.Follow {
			logsTopic := mqtt.JobLogsTopic(cfg.MachineID, cfg.JobID)
			logToken := client.Subscribe(logsTopic, 0, func(_ paho.Client, msg paho.Message) {
				var l model.LogEntry
				if err := json.Unmarshal(msg.Payload(), &l); err == nil && l.JobID == cfg.JobID {
					select {
					case logCh <- l:
					default:
						select {
						case <-logCh:
						default:
						}
						select {
						case logCh <- l:
						default:
						}
					}
				}
			})
			if !logToken.WaitTimeout(5 * time.Second) {
				return nil, fmt.Errorf("subscription timed out for %s", logsTopic)
			}
			if err := logToken.Error(); err != nil {
				return nil, fmt.Errorf("subscription failed for %s: %w", logsTopic, err)
			}
		}

		if cfg.OnSubscribed != nil {
			cfg.OnSubscribed()
		}
	}

	// Construct dispatch payload per docs/mqtt-protocol.md §4.4
	dispatchMsg := model.DispatchCommand{
		CommandID:      cfg.CommandID,
		JobID:          cfg.JobID,
		Engine:         cfg.Engine,
		Cwd:            cfg.Cwd,
		Command:        commandBin,
		Args:           commandArgs,
		Title:          cfg.Title,
		TimeoutSeconds: cfg.TimeoutSeconds,
		Env:            cfg.Env,
		RequestedBy:    cfg.RequestedBy,
		Timestamp:      dispatchStartTime,
	}

	payload, err := json.Marshal(dispatchMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode dispatch payload: %w", err)
	}

	dispatchTopic := mqtt.MachineDispatchTopic(cfg.MachineID)
	// QoS 1, Retained false per protocol §3.2
	pubToken := client.Publish(dispatchTopic, 1, false, payload)
	if !pubToken.WaitTimeout(5 * time.Second) {
		return nil, fmt.Errorf("MQTT dispatch publish timed out")
	}
	if err := pubToken.Error(); err != nil {
		return nil, fmt.Errorf("MQTT dispatch publish failed: %w", err)
	}

	if !cfg.Quiet {
		fmt.Fprintf(cfg.Out, "[agentpulse] Dispatched job %s (command: %s) to machine %s\n", cfg.JobID, cfg.CommandID, cfg.MachineID)
	}
	if cfg.OnDispatched != nil {
		cfg.OnDispatched(&dispatchMsg)
	}

	if !cfg.Wait && !cfg.Follow {
		return &dispatchMsg, nil
	}

	// Wait loop for --wait / --follow
	if !cfg.Quiet {
		fmt.Fprintf(cfg.Out, "[agentpulse] Waiting for job %s to complete...\n", cfg.JobID)
	}

	var timeoutTimer *time.Timer
	var timeoutCh <-chan time.Time
	if cfg.WaitTimeoutSeconds > 0 {
		timeoutTimer = time.NewTimer(time.Duration(cfg.WaitTimeoutSeconds) * time.Second)
		defer timeoutTimer.Stop()
		timeoutCh = timeoutTimer.C
	}

	var lastStatus model.JobStatus
	var lastProgress float64 = -1

	for {
		select {
		case <-ctx.Done():
			return &dispatchMsg, ctx.Err()

		case <-timeoutCh:
			if !cfg.Quiet {
				fmt.Fprintf(cfg.ErrOut, "\n[agentpulse] Wait timed out after %ds (job %s may still be running remotely)\n", cfg.WaitTimeoutSeconds, cfg.JobID)
			}
			return &dispatchMsg, fmt.Errorf("wait timed out after %ds", cfg.WaitTimeoutSeconds)

		case l := <-logCh:
			if cfg.Follow {
				fmt.Fprintf(cfg.Out, "[%s] [%s] %s\n", l.JobID, l.Level, l.Message)
			}
			if cfg.OnLog != nil {
				cfg.OnLog(l)
			}

		case j := <-statusCh:
			ts := j.UpdatedAt
			if ts.IsZero() {
				ts = j.StartedAt
			}
			if !ts.IsZero() && ts.Before(dispatchStartTime) {
				continue
			}

			if cfg.OnProgress != nil {
				cfg.OnProgress(j)
			}

			// Report progress change
			if j.Status != lastStatus || j.Progress != lastProgress {
				if !cfg.Quiet {
					if j.Status != lastStatus {
						fmt.Fprintf(cfg.Out, "[agentpulse] Job %s status: %s (progress: %.1f%%)\n", j.ID, j.Status, j.Progress)
					} else if (j.Progress-lastProgress >= 5.0) || j.Progress == 100.0 {
						fmt.Fprintf(cfg.Out, "[agentpulse] Job %s progress: %.1f%%\n", j.ID, j.Progress)
					}
				}
				lastStatus = j.Status
				lastProgress = j.Progress
			}

			if j.Status == model.StatusDone {
				dur := j.DurationStr
				if dur == "" && !j.StartedAt.IsZero() {
					dur = j.UpdatedAt.Sub(j.StartedAt).Round(time.Second).String()
				}
				if !cfg.Quiet {
					if dur != "" {
						fmt.Fprintf(cfg.Out, "\n[agentpulse] Job %s completed successfully in %s (status: done)\n", j.ID, dur)
					} else {
						fmt.Fprintf(cfg.Out, "\n[agentpulse] Job %s completed successfully (status: done)\n", j.ID)
					}
				}
				return &dispatchMsg, nil
			}

			if j.Status == model.StatusFailed {
				errMsg := j.Error
				if errMsg == "" {
					errMsg = "job failed without error message"
				}
				if !cfg.Quiet {
					fmt.Fprintf(cfg.ErrOut, "\n[agentpulse] Job %s failed: %s\n", j.ID, errMsg)
				}
				return &dispatchMsg, fmt.Errorf("job %s failed: %s", j.ID, errMsg)
			}
		}
	}
}

// verifyTargetMachineOnClient queries retained machine status and announcement on the existing MQTT client connection.
func verifyTargetMachineOnClient(ctx context.Context, client paho.Client, machineID, agentName string, settleTimeout, heartbeatTimeout time.Duration) error {
	statusTopic := mqtt.MachineStatusTopic(machineID)
	announceTopic := mqtt.MachineAnnounceTopic(machineID)
	receivedMachine := make(chan *model.Machine, 1)
	receivedAnnounce := make(chan *model.MachineAnnounce, 1)

	subToken := client.Subscribe(statusTopic, 1, func(_ paho.Client, msg paho.Message) {
		var m model.Machine
		if err := json.Unmarshal(msg.Payload(), &m); err == nil {
			if m.ID == "" {
				m.ID = machineID
			}
			if m.ID == machineID {
				select {
				case receivedMachine <- &m:
				default:
				}
			}
		}
	})
	if !subToken.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("MQTT subscription timed out for %s", statusTopic)
	}
	if err := subToken.Error(); err != nil {
		return fmt.Errorf("MQTT subscription failed for %s: %w", statusTopic, err)
	}
	defer func() {
		unsub := client.Unsubscribe(statusTopic)
		if unsub != nil {
			unsub.WaitTimeout(time.Second)
		}
	}()

	if agentName != "" {
		annToken := client.Subscribe(announceTopic, 1, func(_ paho.Client, msg paho.Message) {
			var ann model.MachineAnnounce
			if err := json.Unmarshal(msg.Payload(), &ann); err == nil {
				if ann.MachineID == "" {
					ann.MachineID = machineID
				}
				if ann.MachineID == machineID {
					select {
					case receivedAnnounce <- &ann:
					default:
					}
				}
			}
		})
		if annToken.WaitTimeout(5 * time.Second) && annToken.Error() == nil {
			defer func() {
				unsub := client.Unsubscribe(announceTopic)
				if unsub != nil {
					unsub.WaitTimeout(time.Second)
				}
			}()
		}
	}

	var targetMachine *model.Machine
	select {
	case m := <-receivedMachine:
		targetMachine = m
	case <-time.After(settleTimeout):
		return fmt.Errorf("target machine %q is unknown (never registered or no heartbeat seen); ensure 'agentpulse' is active on target node, or use --force to bypass", machineID)
	case <-ctx.Done():
		return ctx.Err()
	}

	if heartbeatTimeout <= 0 {
		heartbeatTimeout = 45 * time.Second
	}
	if !targetMachine.Online || (!targetMachine.LastHeartbeat.IsZero() && time.Since(targetMachine.LastHeartbeat) > heartbeatTimeout) {
		return fmt.Errorf("target machine %q is offline (last heartbeat %s); check node listen status and connectivity, or use --force to bypass", machineID, targetMachine.HeartbeatAgo())
	}

	if agentName != "" {
		select {
		case ann := <-receivedAnnounce:
			ag, ok := ann.FindAgent(agentName)
			if !ok || !ag.Ok {
				detail := "not announced"
				if ok {
					detail = ag.Detail
				}
				return fmt.Errorf("target machine %q agent %q is not available (ok: false: %s); check doctor or use --force to bypass", machineID, agentName, detail)
			}
		case <-time.After(settleTimeout):
			return fmt.Errorf("target machine %q has no announce data (cannot verify agent %q); check doctor or use --force to bypass", machineID, agentName)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

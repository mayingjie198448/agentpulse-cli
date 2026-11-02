package wrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

// ListenerConfig configures the dispatch command listener daemon.
type ListenerConfig struct {
	MachineID                  string
	MachineName                string
	HeartbeatInterval          time.Duration
	StatusFile                 string
	MQTT                       *mqtt.Config
	MQTTClient                 paho.Client
	Stub                       bool
	DangerouslySkipPermissions bool
	Quiet                      bool
	Stdout                     io.Writer
	Stderr                     io.Writer
	OnMQTTConnected            func()
	OnListening                func(topic string)
	OnJobStart                 func(job model.Job)
	OnJobComplete              func(job model.Job, err error)
}

// Listener is the background daemon that listens for dispatch commands over MQTT,
// manages node heartbeats and LWT, and executes jobs serially via thin wrapper runs.
//
// Concurrency design:
// Jobs are queued and executed serially (one job at a time) to prevent resource contention
// and conflicting workspace file mutations across autonomous agent invocations.
type Listener struct {
	cfg        ListenerConfig
	mu         sync.Mutex
	machine    model.Machine
	mqttClient paho.Client
	ownsMQTT   bool
	dispatchCh chan model.DispatchCommand
	stopped    atomic.Bool
	stopCh     chan struct{}

	heartbeatRunning bool
	stopHeartbeat    chan struct{}
	heartbeatDone    chan struct{}
}

// NewListener initializes a new Listener instance.
func NewListener(cfg ListenerConfig) (*Listener, error) {
	if cfg.MachineID == "" {
		cfg.MachineID = DetectMachineID()
	}
	if cfg.MachineName == "" {
		cfg.MachineName = cfg.MachineID
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if cfg.StatusFile == "" {
		cfg.StatusFile = DefaultStatusFile
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}

	now := time.Now().UTC()
	machine := model.Machine{
		ID:            cfg.MachineID,
		Name:          cfg.MachineName,
		Online:        true,
		LastHeartbeat: now,
		Platform:      fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		IP:            DetectLocalIP(),
		JobCount:      0,
	}

	return &Listener{
		cfg:           cfg,
		machine:       machine,
		dispatchCh:    make(chan model.DispatchCommand, 64),
		stopCh:        make(chan struct{}),
		stopHeartbeat: make(chan struct{}),
		heartbeatDone: make(chan struct{}),
	}, nil
}

// Machine returns a snapshot of the listener's machine state.
func (l *Listener) Machine() model.Machine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.machine
}

// Run executes the listener: connects MQTT with LWT, subscribes to dispatch commands,
// sends periodic heartbeats, and processes jobs sequentially.
func (l *Listener) Run(ctx context.Context) (runErr error) {
	if err := l.setupMQTT(); err != nil {
		return fmt.Errorf("MQTT setup failed: %w", err)
	}

	defer func() {
		l.stopHeartbeatLoop()

		// Publish final offline status
		l.mu.Lock()
		l.machine.Online = false
		l.machine.JobCount = 0
		l.machine.LastHeartbeat = time.Now().UTC()
		l.mu.Unlock()

		if l.mqttClient != nil && l.mqttClient.IsConnected() {
			l.publishMachineStatus()
			if l.ownsMQTT {
				l.mqttClient.Disconnect(100)
			}
		}
	}()

	// Publish initial online status
	l.publishMachineStatus()

	// Start periodic heartbeat loop
	l.startHeartbeatLoop()

	// Subscribe to dispatch command topic (QoS 1)
	dispatchTopic := mqtt.MachineDispatchTopic(l.cfg.MachineID)
	token := l.mqttClient.Subscribe(dispatchTopic, 1, l.handleDispatchMessage)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("MQTT subscription timed out for topic %s", dispatchTopic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT subscription failed for topic %s: %w", dispatchTopic, err)
	}

	if l.cfg.OnListening != nil {
		l.cfg.OnListening(dispatchTopic)
	}

	// Worker loop: process incoming dispatch commands serially
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.stopCh:
				return
			case cmd, ok := <-l.dispatchCh:
				if !ok {
					return
				}
				l.executeJob(ctx, cmd)
			}
		}
	}()

	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case <-l.stopCh:
		runErr = nil
	}

	l.stopped.Store(true)
	<-workerDone

	return runErr
}

// Stop signals the listener to shut down cleanly.
func (l *Listener) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.stopCh:
	default:
		close(l.stopCh)
	}
}

// setupMQTT establishes the MQTT connection and registers LWT per docs/mqtt-protocol.md §4.1.
func (l *Listener) setupMQTT() error {
	if l.cfg.MQTTClient != nil {
		l.mqttClient = l.cfg.MQTTClient
		l.ownsMQTT = false
		if l.cfg.OnMQTTConnected != nil {
			l.cfg.OnMQTTConnected()
		}
		return nil
	}

	if l.cfg.MQTT == nil || l.cfg.MQTT.BrokerURL == "" {
		return fmt.Errorf("MQTT broker URL is required for listener")
	}

	cfg := *l.cfg.MQTT
	if cfg.ClientID == "" {
		cfg.ClientID = fmt.Sprintf("agentpulse-listener-%s-%d", l.cfg.MachineID, os.Getpid())
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(cfg.BrokerURL)
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	opts.SetKeepAlive(cfg.KeepAlive)
	opts.SetConnectTimeout(cfg.Timeout)
	opts.SetAutoReconnect(true)

	// Configure LWT: QoS 1, Retained true, online: false, job_count: 0
	lwtTopic := mqtt.MachineStatusTopic(l.cfg.MachineID)
	lwtMachine := l.machine
	lwtMachine.Online = false
	lwtMachine.JobCount = 0
	lwtPayload, _ := json.Marshal(lwtMachine)
	opts.SetWill(lwtTopic, string(lwtPayload), 1, true)

	client := paho.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(cfg.Timeout) {
		return fmt.Errorf("MQTT connection to %s timed out after %v", cfg.BrokerURL, cfg.Timeout)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT connection to %s failed: %w", cfg.BrokerURL, err)
	}

	l.mqttClient = client
	l.ownsMQTT = true
	if l.cfg.OnMQTTConnected != nil {
		l.cfg.OnMQTTConnected()
	}
	return nil
}

// handleDispatchMessage receives and validates MQTT dispatch messages.
func (l *Listener) handleDispatchMessage(_ paho.Client, msg paho.Message) {
	if l.stopped.Load() {
		return
	}

	var cmd model.DispatchCommand
	if err := json.Unmarshal(msg.Payload(), &cmd); err != nil {
		l.logError(fmt.Sprintf("[agentpulse listener] rejected malformed dispatch payload: %v", err))
		return
	}

	// Validate command
	if strings.TrimSpace(cmd.Command) == "" && len(cmd.Args) == 0 {
		l.logError("[agentpulse listener] rejected invalid dispatch: command is empty")
		return
	}

	if cmd.JobID == "" {
		cmd.JobID = GenerateJobID(cmd.Engine)
	}
	if cmd.CommandID == "" {
		cmd.CommandID = GenerateCommandID()
	}

	select {
	case l.dispatchCh <- cmd:
		if !l.cfg.Quiet {
			l.log(fmt.Sprintf("[agentpulse listener] queued dispatch command %s for job %s", cmd.CommandID, cmd.JobID))
		}
	default:
		l.logError(fmt.Sprintf("[agentpulse listener] serial queue full (64), rejected command %s", cmd.CommandID))
	}
}

// executeJob executes a single dispatch command via wrapper.Run.
func (l *Listener) executeJob(ctx context.Context, cmd model.DispatchCommand) {
	args := BuildCommandArgs(cmd.Command, cmd.Args)
	if l.cfg.DangerouslySkipPermissions && len(args) > 0 && filepath.Base(args[0]) == "agy" {
		hasPerm := false
		for _, a := range args {
			if a == "--dangerously-skip-permissions" {
				hasPerm = true
				break
			}
		}
		if !hasPerm {
			args = append([]string{args[0], "--dangerously-skip-permissions"}, args[1:]...)
		}
	}

	jobCtx := ctx
	var cancel context.CancelFunc
	if cmd.TimeoutSeconds > 0 {
		jobCtx, cancel = context.WithTimeout(ctx, time.Duration(cmd.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// Update machine job count: 1
	l.mu.Lock()
	l.machine.JobCount = 1
	l.machine.LastHeartbeat = time.Now().UTC()
	l.mu.Unlock()
	l.publishMachineStatus()

	w, err := New(Config{
		MachineID:         l.cfg.MachineID,
		MachineName:       l.cfg.MachineName,
		JobID:             cmd.JobID,
		Engine:            cmd.Engine,
		Title:             cmd.Title,
		Args:              args,
		Cwd:               cmd.Cwd,
		Env:               cmd.Env,
		StatusFile:        l.cfg.StatusFile,
		HeartbeatInterval: l.cfg.HeartbeatInterval,
		MQTTClient:        l.mqttClient,
		MirrorStdout:      l.cfg.Stdout,
		MirrorStderr:      l.cfg.Stderr,
		Stub:              l.cfg.Stub,
		Quiet:             l.cfg.Quiet,
		KeepMachineOnline: true,
		OnExecute: func(cmdStr string) {
			if !l.cfg.Quiet {
				l.log(fmt.Sprintf("[agentpulse listener] executing: %s", cmdStr))
			}
		},
	})
	if err != nil {
		l.logError(fmt.Sprintf("[agentpulse listener] failed to initialize wrapper for job %s: %v", cmd.JobID, err))
		l.mu.Lock()
		l.machine.JobCount = 0
		l.mu.Unlock()
		l.publishMachineStatus()
		return
	}

	if l.cfg.OnJobStart != nil {
		l.cfg.OnJobStart(w.Job())
	}

	runErr := w.Run(jobCtx)

	if l.cfg.OnJobComplete != nil {
		l.cfg.OnJobComplete(w.Job(), runErr)
	}

	// Reset machine job count: 0
	l.mu.Lock()
	l.machine.JobCount = 0
	l.machine.LastHeartbeat = time.Now().UTC()
	l.mu.Unlock()
	l.publishMachineStatus()

	if !l.cfg.Quiet {
		if runErr == nil {
			l.log(fmt.Sprintf("[agentpulse listener] job %s completed successfully in %s (status: done)", cmd.JobID, w.Job().DurationStr))
		} else {
			l.log(fmt.Sprintf("[agentpulse listener] job %s failed in %s: %v", cmd.JobID, w.Job().DurationStr, runErr))
		}
	}
}

func (l *Listener) startHeartbeatLoop() {
	l.mu.Lock()
	if l.heartbeatRunning {
		l.mu.Unlock()
		return
	}
	l.heartbeatRunning = true
	l.mu.Unlock()

	go l.heartbeatLoop()
}

func (l *Listener) heartbeatLoop() {
	defer close(l.heartbeatDone)
	ticker := time.NewTicker(l.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopHeartbeat:
			return
		case <-ticker.C:
			now := time.Now().UTC()
			l.mu.Lock()
			l.machine.LastHeartbeat = now
			l.mu.Unlock()

			l.publishMachineStatus()
		}
	}
}

func (l *Listener) stopHeartbeatLoop() {
	l.mu.Lock()
	if !l.heartbeatRunning {
		l.mu.Unlock()
		return
	}
	l.heartbeatRunning = false
	l.mu.Unlock()

	select {
	case <-l.stopHeartbeat:
	default:
		close(l.stopHeartbeat)
	}

	select {
	case <-l.heartbeatDone:
	case <-time.After(1 * time.Second):
	}
}

func (l *Listener) publishMachineStatus() {
	if l.mqttClient == nil || !l.mqttClient.IsConnected() {
		return
	}
	l.mu.Lock()
	m := l.machine
	l.mu.Unlock()

	topic := mqtt.MachineStatusTopic(m.ID)
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	token := l.mqttClient.Publish(topic, 1, true, data)
	token.WaitTimeout(2 * time.Second)
}

func (l *Listener) log(msg string) {
	if l.cfg.Stdout != nil {
		fmt.Fprintln(l.cfg.Stdout, msg)
	}
}

func (l *Listener) logError(msg string) {
	if l.cfg.Stderr != nil {
		fmt.Fprintln(l.cfg.Stderr, msg)
	}
}

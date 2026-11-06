package wrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
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

	// Bare agent process attach options
	DisableBareAttach      bool
	AttachScanInterval     time.Duration
	Agents                 []config.AgentConfig
	ProcessLister          ProcessLister
	IsProcessAlive         func(pid int) bool
	ProcessExitChecker     func(pid int) (exitCode int, ok bool)
	ReliableSuccessChecker func(pid int) bool
	PIDAllowlist           []int
	PIDFilter              func(pid int) bool
	AgyLogDir              string
	AgyBrainDir            string
	OpenCodeLogDir         string
	StartTimeResolver      func(pid int) time.Time
	CwdResolver            func(pid int) string
}

var jobIDRegex = regexp.MustCompile(`"job_id"\s*:\s*"([^"]+)"`)

func extractJobIDFromPayload(payload []byte) string {
	matches := jobIDRegex.FindSubmatch(payload)
	if len(matches) > 1 {
		return string(matches[1])
	}
	return ""
}

type commandDeduplicator struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newCommandDeduplicator(ttl time.Duration) *commandDeduplicator {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &commandDeduplicator{
		ttl:  ttl,
		seen: make(map[string]time.Time),
	}
}

func (d *commandDeduplicator) SeenOrAdd(commandID string) bool {
	if commandID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if len(d.seen) > 256 {
		for id, t := range d.seen {
			if now.Sub(t) > d.ttl {
				delete(d.seen, id)
			}
		}
	}

	if t, exists := d.seen[commandID]; exists && now.Sub(t) <= d.ttl {
		return true
	}
	d.seen[commandID] = now
	return false
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

	executingJob atomic.Bool
	dedup        *commandDeduplicator

	heartbeatRunning bool
	stopHeartbeat    chan struct{}
	heartbeatDone    chan struct{}

	attachMgr    *AttachManager
	pidsMu       sync.Mutex
	dispatchPIDs map[int]bool
}

// NewListener initializes a new Listener instance.
func NewListener(cfg ListenerConfig) (*Listener, error) {
	if cfg.MachineID == "" {
		cfg.MachineID = DetectMachineID()
	} else {
		cfg.MachineID = SanitizeID(cfg.MachineID)
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

	l := &Listener{
		cfg:           cfg,
		machine:       machine,
		dispatchCh:    make(chan model.DispatchCommand, 64),
		stopCh:        make(chan struct{}),
		stopHeartbeat: make(chan struct{}),
		heartbeatDone: make(chan struct{}),
		dedup:         newCommandDeduplicator(15 * time.Minute),
		dispatchPIDs:  make(map[int]bool),
	}

	if !cfg.DisableBareAttach {
		agents := cfg.Agents
		if len(agents) == 0 {
			if fileCfg, err := config.LoadConfig(""); err == nil && len(fileCfg.Agents) > 0 {
				agents = fileCfg.Agents
			} else {
				agents = config.DefaultAgents()
			}
		}

		attachCfg := AttachManagerConfig{
			MachineID:              cfg.MachineID,
			Agents:                 agents,
			ScanInterval:           cfg.AttachScanInterval,
			ProcessLister:          cfg.ProcessLister,
			IsProcessAlive:         cfg.IsProcessAlive,
			ProcessExitChecker:     cfg.ProcessExitChecker,
			ReliableSuccessChecker: cfg.ReliableSuccessChecker,
			PIDAllowlist:           cfg.PIDAllowlist,
			PIDFilter:              cfg.PIDFilter,
			AgyLogDir:              cfg.AgyLogDir,
			AgyBrainDir:            cfg.AgyBrainDir,
			OpenCodeLogDir:         cfg.OpenCodeLogDir,
			StatusFile:             cfg.StatusFile,
			Quiet:                  cfg.Quiet,
			Stdout:                 cfg.Stdout,
			Stderr:                 cfg.Stderr,
			StartTimeResolver:      cfg.StartTimeResolver,
			CwdResolver:            cfg.CwdResolver,
			IsDispatchPID: func(pid int) bool {
				l.pidsMu.Lock()
				defer l.pidsMu.Unlock()
				return l.dispatchPIDs[pid]
			},
			OnJobCountChange: func() {
				l.mu.Lock()
				l.machine.JobCount = l.currentJobCount()
				l.mu.Unlock()
				l.publishMachineStatus()
			},
		}
		l.attachMgr = NewAttachManager(attachCfg)
	}

	return l, nil
}

// AttachManager returns the listener's bare process attach manager instance, if enabled.
func (l *Listener) AttachManager() *AttachManager {
	return l.attachMgr
}

// currentJobCount returns the number of currently running jobs (0 or 1) + jobs queued in dispatchCh + bare attached jobs.
func (l *Listener) currentJobCount() int {
	count := len(l.dispatchCh)
	if l.executingJob.Load() {
		count++
	}
	if l.attachMgr != nil {
		count += l.attachMgr.ActiveCount()
	}
	return count
}

func (l *Listener) publishJobQueued(cmd model.DispatchCommand) {
	if l.mqttClient == nil || !l.mqttClient.IsConnected() || cmd.JobID == "" {
		return
	}
	now := time.Now().UTC()
	if !cmd.Timestamp.IsZero() && now.Before(cmd.Timestamp) {
		now = cmd.Timestamp
	}
	cmdStr := strings.TrimSpace(cmd.Command)
	if len(cmd.Args) > 0 {
		if cmdStr == "" {
			cmdStr = strings.Join(cmd.Args, " ")
		} else {
			cmdStr = cmdStr + " " + strings.Join(cmd.Args, " ")
		}
	}
	job := model.Job{
		ID:        cmd.JobID,
		MachineID: l.cfg.MachineID,
		Engine:    cmd.Engine,
		Status:    model.StatusQueued,
		Progress:  0.0,
		Cwd:       cmd.Cwd,
		Command:   cmdStr,
		Title:     cmd.Title,
		StartedAt: now,
		UpdatedAt: now,
	}
	topic := mqtt.JobStatusTopic(l.cfg.MachineID, cmd.JobID)
	data, err := json.Marshal(job)
	if err != nil {
		return
	}
	token := l.mqttClient.Publish(topic, 1, true, data)
	token.WaitTimeout(2 * time.Second)
}

func (l *Listener) publishJobFailed(jobID string, engine model.EngineType, cwd, command, title, errMsg string) {
	if l.mqttClient == nil || !l.mqttClient.IsConnected() || jobID == "" {
		return
	}
	now := time.Now().UTC()
	job := model.Job{
		ID:          jobID,
		MachineID:   l.cfg.MachineID,
		Engine:      engine,
		Status:      model.StatusFailed,
		Progress:    0.0,
		Cwd:         cwd,
		Command:     command,
		Title:       title,
		StartedAt:   now,
		UpdatedAt:   now,
		DurationStr: "0s",
		Error:       errMsg,
	}
	topic := mqtt.JobStatusTopic(l.cfg.MachineID, jobID)
	data, err := json.Marshal(job)
	if err != nil {
		return
	}
	token := l.mqttClient.Publish(topic, 1, true, data)
	token.WaitTimeout(2 * time.Second)
}

func (l *Listener) onConnect(c paho.Client) {
	if l.stopped.Load() || c == nil || !c.IsConnected() {
		return
	}
	dispatchTopic := mqtt.MachineDispatchTopic(l.cfg.MachineID)
	token := c.Subscribe(dispatchTopic, 1, l.handleDispatchMessage)
	if !token.WaitTimeout(5 * time.Second) {
		l.logError(fmt.Sprintf("[agentpulse listener] subscription timed out for topic %s on (re)connect", dispatchTopic))
	} else if err := token.Error(); err != nil {
		l.logError(fmt.Sprintf("[agentpulse listener] subscription failed for topic %s on (re)connect: %v", dispatchTopic, err))
	} else if l.cfg.OnListening != nil {
		l.cfg.OnListening(dispatchTopic)
	}

	l.mu.Lock()
	l.machine.Online = true
	l.machine.JobCount = l.currentJobCount()
	l.machine.LastHeartbeat = time.Now().UTC()
	l.mu.Unlock()
	l.publishMachineStatus()
}

// OnConnect handles initial connect or auto-reconnect events,
// re-subscribing to the dispatch topic and republishing online status.
func (l *Listener) OnConnect(c paho.Client) {
	l.onConnect(c)
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
		if l.attachMgr != nil {
			l.attachMgr.Stop()
		}

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

	if l.attachMgr != nil {
		l.attachMgr.SetMQTTClient(l.mqttClient)
		l.attachMgr.Start(ctx)
	}

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
	if l.attachMgr != nil {
		l.attachMgr.Stop()
	}
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
		cfg.Timeout = 10 * time.Second
	}

	opts := paho.NewClientOptions()
	if err := mqtt.ApplyClientOptions(opts, cfg); err != nil {
		return fmt.Errorf("MQTT client options error: %w", err)
	}
	opts.SetAutoReconnect(true)
	opts.SetResumeSubs(true)

	// Configure LWT: QoS 1, Retained true, online: false, job_count: 0
	lwtTopic := mqtt.MachineStatusTopic(l.cfg.MachineID)
	lwtMachine := l.machine
	lwtMachine.Online = false
	lwtMachine.JobCount = 0
	lwtPayload, _ := json.Marshal(lwtMachine)
	opts.SetWill(lwtTopic, string(lwtPayload), 1, true)

	// Resubscribe automatically upon reconnection per docs/mqtt-protocol.md
	opts.SetOnConnectHandler(func(c paho.Client) {
		l.onConnect(c)
	})

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
		jobID := extractJobIDFromPayload(msg.Payload())
		if jobID != "" {
			l.publishJobFailed(jobID, "", "", "", "", fmt.Sprintf("rejected malformed dispatch payload: %v", err))
		}
		return
	}

	// Validate command
	if strings.TrimSpace(cmd.Command) == "" && len(cmd.Args) == 0 {
		errMsg := "rejected invalid dispatch: command is empty"
		l.logError("[agentpulse listener] " + errMsg)
		if cmd.JobID != "" {
			l.publishJobFailed(cmd.JobID, cmd.Engine, cmd.Cwd, "", cmd.Title, errMsg)
		}
		return
	}

	if cmd.JobID == "" {
		cmd.JobID = GenerateJobID(cmd.Engine)
	}
	if cmd.CommandID == "" {
		cmd.CommandID = GenerateCommandID()
	}

	// Deduplicate by command_id (QoS 1 redelivery)
	if cmd.CommandID != "" && l.dedup.SeenOrAdd(cmd.CommandID) {
		if !l.cfg.Quiet {
			l.log(fmt.Sprintf("[agentpulse listener] ignored duplicate dispatch command: %s (job %s)", cmd.CommandID, cmd.JobID))
		}
		return
	}

	select {
	case l.dispatchCh <- cmd:
		l.mu.Lock()
		l.machine.JobCount = l.currentJobCount()
		l.machine.LastHeartbeat = time.Now().UTC()
		l.mu.Unlock()
		l.publishMachineStatus()
		l.publishJobQueued(cmd)

		if !l.cfg.Quiet {
			l.log(fmt.Sprintf("[agentpulse listener] queued dispatch command %s for job %s", cmd.CommandID, cmd.JobID))
		}
	default:
		errMsg := fmt.Sprintf("rejected dispatch: serial execution queue is full (64) for command %s", cmd.CommandID)
		l.logError(fmt.Sprintf("[agentpulse listener] %s", errMsg))
		if cmd.JobID != "" {
			l.publishJobFailed(cmd.JobID, cmd.Engine, cmd.Cwd, cmd.Command, cmd.Title, errMsg)
		}
	}
}

// executeJob executes a single dispatch command via wrapper.Run.
func (l *Listener) executeJob(ctx context.Context, cmd model.DispatchCommand) {
	args := BuildCommandArgs(cmd.Command, cmd.Args)
	if len(args) > 0 {
		fileCfg, _ := config.LoadConfig("")
		resolved := config.ResolveAgentBinary(fileCfg, args[0])
		if resolved != "" {
			args[0] = resolved
		}
	}
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

	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// Cancel job immediately if listener is stopped or context is canceled
	go func() {
		select {
		case <-l.stopCh:
			jobCancel()
		case <-jobCtx.Done():
		}
	}()

	if cmd.TimeoutSeconds > 0 {
		timeoutCtx, timeoutCancel := context.WithTimeout(jobCtx, time.Duration(cmd.TimeoutSeconds)*time.Second)
		defer timeoutCancel()
		jobCtx = timeoutCtx
	}

	l.executingJob.Store(true)
	defer l.executingJob.Store(false)

	// Update machine job count (running + queued)
	l.mu.Lock()
	l.machine.JobCount = l.currentJobCount()
	l.machine.LastHeartbeat = time.Now().UTC()
	l.mu.Unlock()
	l.publishMachineStatus()

	now := time.Now().UTC()
	if !cmd.Timestamp.IsZero() && now.Before(cmd.Timestamp) {
		now = cmd.Timestamp
	}

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
		StartedAt:         now,
		OnExecute: func(cmdStr string) {
			if !l.cfg.Quiet {
				l.log(fmt.Sprintf("[agentpulse listener] executing: %s", cmdStr))
			}
		},
		OnProcessStart: func(pid int) {
			l.pidsMu.Lock()
			l.dispatchPIDs[pid] = true
			l.pidsMu.Unlock()
		},
	})
	if err != nil {
		l.logError(fmt.Sprintf("[agentpulse listener] failed to initialize wrapper for job %s: %v", cmd.JobID, err))
		l.executingJob.Store(false)
		l.mu.Lock()
		l.machine.JobCount = l.currentJobCount()
		l.mu.Unlock()
		l.publishMachineStatus()
		return
	}

	if l.cfg.OnJobStart != nil {
		l.cfg.OnJobStart(w.Job())
	}

	runErr := w.Run(jobCtx)

	if pid := w.PID(); pid > 0 {
		l.pidsMu.Lock()
		delete(l.dispatchPIDs, pid)
		l.pidsMu.Unlock()
	}

	if l.cfg.OnJobComplete != nil {
		l.cfg.OnJobComplete(w.Job(), runErr)
	}

	// Update machine job count based on remaining queue
	l.executingJob.Store(false)
	l.mu.Lock()
	l.machine.JobCount = l.currentJobCount()
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
			l.machine.JobCount = l.currentJobCount()
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

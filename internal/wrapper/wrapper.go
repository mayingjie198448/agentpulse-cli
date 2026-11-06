package wrapper

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

const (
	// DefaultHeartbeatInterval is the frequency of node heartbeats (aligns with docs/mqtt-protocol.md §4.1).
	DefaultHeartbeatInterval = 15 * time.Second

	// DefaultStatusFile is the default local file path to write machine & job status snapshots.
	DefaultStatusFile = ".agentpulse/status.json"

	// MaxLogMessageBytes specifies the maximum size of a single log message payload (64 KB per protocol §6.3).
	MaxLogMessageBytes = 64 * 1024

	// TruncateSuffix is appended when a log line exceeds MaxLogMessageBytes.
	TruncateSuffix = "...[truncated by agentpulse wrapper]"
)

// LocalStatus represents the on-disk JSON snapshot of local wrapper state.
type LocalStatus = model.LocalStatus

// Config configures the execution of a thin wrapper instance.
type Config struct {
	MachineID         string
	MachineName       string
	JobID             string
	Engine            model.EngineType // "agy" or "opencode"
	Title             string
	Command           string
	Args              []string
	Cwd               string
	StatusFile        string
	HeartbeatInterval time.Duration
	MQTT              *mqtt.Config
	MQTTClient        paho.Client
	MirrorStdout      io.Writer
	MirrorStderr      io.Writer
	Stub              bool
	Quiet             bool
	Env               map[string]string
	KeepMachineOnline bool
	StartedAt         time.Time
	OnMQTTConnected   func()
	OnExecute         func(cmdStr string)
	OnProcessStart    func(pid int)
}

// Wrapper is the thin local process managing local agent execution, local status writing,
// and MQTT telemetry publishing.
type Wrapper struct {
	cfg        Config
	mu         sync.Mutex
	writeMu    sync.Mutex
	machine    model.Machine
	job        model.Job
	pid        int
	seq        int64
	mqttClient paho.Client
	ownsMQTT   bool

	heartbeatRunning bool
	stopHeartbeat    chan struct{}
	heartbeatDone    chan struct{}
}

// New creates and initializes a Wrapper instance.
func New(cfg Config) (*Wrapper, error) {
	// 1. Populate defaults
	if cfg.Cwd == "" {
		if cwd, err := os.Getwd(); err == nil {
			cfg.Cwd = cwd
		} else {
			cfg.Cwd = "."
		}
	}

	if cfg.MachineID == "" {
		cfg.MachineID = detectMachineID()
	} else {
		cfg.MachineID = SanitizeID(cfg.MachineID)
	}
	if cfg.MachineName == "" {
		cfg.MachineName = cfg.MachineID
	}

	if cfg.Engine == "" {
		cfg.Engine = detectEngine(cfg.Args)
	}

	if cfg.JobID == "" {
		cfg.JobID = generateJobID(cfg.Engine)
	}

	if cfg.Title == "" {
		cfg.Title = inferTitle(cfg.Args, cfg.Command)
	}

	if cfg.Command == "" && len(cfg.Args) > 0 {
		cfg.Command = strings.Join(cfg.Args, " ")
	}

	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = DefaultHeartbeatInterval
	}

	if cfg.StatusFile == "" {
		cfg.StatusFile = DefaultStatusFile
	}

	now := time.Now().UTC()
	if !cfg.StartedAt.IsZero() && now.Before(cfg.StartedAt) {
		now = cfg.StartedAt
	}
	machine := model.Machine{
		ID:            cfg.MachineID,
		Name:          cfg.MachineName,
		Online:        true,
		LastHeartbeat: now,
		Platform:      fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		IP:            detectLocalIP(),
		JobCount:      1,
	}

	job := model.Job{
		ID:        cfg.JobID,
		MachineID: cfg.MachineID,
		Engine:    cfg.Engine,
		Status:    model.StatusRunning,
		Progress:  0.0,
		Cwd:       cfg.Cwd,
		Command:   cfg.Command,
		Title:     cfg.Title,
		StartedAt: now,
		UpdatedAt: now,
	}

	w := &Wrapper{
		cfg:           cfg,
		machine:       machine,
		job:           job,
		stopHeartbeat: make(chan struct{}),
		heartbeatDone: make(chan struct{}),
	}

	return w, nil
}

// Machine returns a snapshot of the wrapper's machine state.
func (w *Wrapper) Machine() model.Machine {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.machine
}

// Job returns a snapshot of the wrapper's job state.
func (w *Wrapper) Job() model.Job {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.job
}

// PID returns the OS process ID of the running child process, or 0 if not running.
func (w *Wrapper) PID() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pid
}

// Run executes the wrapped process or stub, writes local status, and publishes MQTT telemetry.
func (w *Wrapper) Run(ctx context.Context) (runErr error) {
	// Write initial local status snapshot (status: running, online: true, job_count: 1)
	_ = w.writeStatusFile()

	defer func() {
		// Stop heartbeat loop
		w.stopHeartbeatLoop()

		// Finalize job and machine status
		w.mu.Lock()
		finishTime := time.Now().UTC()
		w.job.UpdatedAt = finishTime
		w.job.DurationStr = finishTime.Sub(w.job.StartedAt).Round(time.Second).String()
		if runErr != nil {
			w.job.Status = model.StatusFailed
			w.job.Error = runErr.Error()
		} else {
			w.job.Status = model.StatusDone
			w.job.Progress = 100.0
			w.job.Error = ""
		}
		if !w.cfg.KeepMachineOnline {
			w.machine.Online = false
		}
		w.machine.JobCount = 0
		w.machine.LastHeartbeat = finishTime
		w.mu.Unlock()

		// Write final local status snapshot to disk so status never stays running
		_ = w.writeStatusFile()

		// Final MQTT publications if connected
		if w.mqttClient != nil && w.mqttClient.IsConnected() {
			w.publishJobStatus()
			w.publishAlert()
			if !w.cfg.KeepMachineOnline {
				w.publishMachineStatus()
			}

			if w.ownsMQTT {
				w.mqttClient.Disconnect(100)
			}
		}
	}()

	// Setup MQTT if configured
	if err := w.setupMQTT(); err != nil {
		runErr = fmt.Errorf("MQTT setup failed: %w", err)
		return runErr
	}

	// Publish initial MQTT states if connected
	if w.mqttClient != nil && w.mqttClient.IsConnected() {
		if !w.cfg.KeepMachineOnline {
			w.publishMachineStatus()
		}
		w.publishJobStatus()
	}

	// Start heartbeat loop (refreshes local status periodically, and publishes MQTT heartbeat if connected)
	w.startHeartbeatLoop()

	if w.cfg.OnExecute != nil {
		if w.cfg.Stub {
			w.cfg.OnExecute(fmt.Sprintf("%s (stub)", w.cfg.Engine))
		} else if len(w.cfg.Args) > 0 {
			w.cfg.OnExecute(strings.Join(w.cfg.Args, " "))
		}
	}

	// Execute command or stub
	if w.cfg.Stub {
		runErr = w.runStub(ctx)
	} else {
		runErr = w.runProcess(ctx)
	}

	return runErr
}

// setupMQTT establishes MQTT connection if configured.
func (w *Wrapper) setupMQTT() error {
	if w.cfg.MQTTClient != nil {
		w.mqttClient = w.cfg.MQTTClient
		w.ownsMQTT = false
		if w.cfg.OnMQTTConnected != nil {
			w.cfg.OnMQTTConnected()
		}
		return nil
	}

	if w.cfg.MQTT == nil || w.cfg.MQTT.BrokerURL == "" {
		return nil
	}

	cfg := *w.cfg.MQTT
	if cfg.ClientID == "" {
		cfg.ClientID = fmt.Sprintf("agentpulse-wrapper-%s-%d", w.cfg.MachineID, os.Getpid())
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

	// Configure LWT per protocol §4.1: QoS 1, Retained true, online: false
	lwtTopic := fmt.Sprintf("agentpulse/v1/machines/%s/status", w.cfg.MachineID)
	lwtMachine := w.machine
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

	w.mqttClient = client
	w.ownsMQTT = true
	if w.cfg.OnMQTTConnected != nil {
		w.cfg.OnMQTTConnected()
	}
	return nil
}

func (w *Wrapper) startHeartbeatLoop() {
	w.mu.Lock()
	if w.heartbeatRunning {
		w.mu.Unlock()
		return
	}
	w.heartbeatRunning = true
	w.mu.Unlock()

	go w.heartbeatLoop()
}

// heartbeatLoop sends periodic heartbeats and refreshes the local status file.
func (w *Wrapper) heartbeatLoop() {
	defer close(w.heartbeatDone)
	ticker := time.NewTicker(w.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopHeartbeat:
			return
		case <-ticker.C:
			now := time.Now().UTC()
			w.mu.Lock()
			w.machine.LastHeartbeat = now
			w.job.UpdatedAt = now
			w.mu.Unlock()

			if !w.cfg.KeepMachineOnline {
				w.publishMachineStatus()
			}
			_ = w.writeStatusFile()
		}
	}
}

func (w *Wrapper) stopHeartbeatLoop() {
	w.mu.Lock()
	if !w.heartbeatRunning {
		w.mu.Unlock()
		return
	}
	w.heartbeatRunning = false
	w.mu.Unlock()

	select {
	case <-w.stopHeartbeat:
	default:
		close(w.stopHeartbeat)
	}

	select {
	case <-w.heartbeatDone:
	case <-time.After(1 * time.Second):
	}
}

// runProcess executes the wrapped binary, capturing stdout and stderr.
func (w *Wrapper) runProcess(ctx context.Context) error {
	if len(w.cfg.Args) == 0 {
		return fmt.Errorf("no command provided to wrap")
	}

	bin := w.cfg.Args[0]
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("command binary %q not found: %w", bin, err)
	}

	cmd := exec.CommandContext(ctx, bin, w.cfg.Args[1:]...)
	cmd.Dir = w.cfg.Cwd
	cmdEnv := config.FilterChildProcessEnv(os.Environ())
	if len(w.cfg.Env) > 0 {
		for k, v := range w.cfg.Env {
			cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
		}
	}
	cmd.Env = config.FilterChildProcessEnv(cmdEnv)

	stdoutWriter := newLineWriter(func(line string) {
		w.handleOutputLine(line, false)
	}, w.cfg.MirrorStdout)

	stderrWriter := newLineWriter(func(line string) {
		w.handleOutputLine(line, true)
	}, w.cfg.MirrorStderr)

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process %q: %w", w.cfg.Args[0], err)
	}

	w.mu.Lock()
	if cmd.Process != nil {
		w.pid = cmd.Process.Pid
		if w.cfg.OnProcessStart != nil {
			w.cfg.OnProcessStart(w.pid)
		}
	}
	w.mu.Unlock()
	_ = w.writeStatusFile()

	err := cmd.Wait()
	stdoutWriter.Flush()
	stderrWriter.Flush()
	return err
}

// runStub runs simulated agent execution for testing or fallback when an engine CLI is unavailable.
func (w *Wrapper) runStub(ctx context.Context) error {
	steps := []struct {
		delay    time.Duration
		level    model.LogLevel
		message  string
		progress float64
	}{
		{20 * time.Millisecond, model.LevelAgent, fmt.Sprintf("Initializing %s agent execution environment...", w.cfg.Engine), 10.0},
		{40 * time.Millisecond, model.LevelAgent, fmt.Sprintf("Analyzing task: %s", w.cfg.Title), 30.0},
		{40 * time.Millisecond, model.LevelTool, "Executing tool: git status", 50.0},
		{40 * time.Millisecond, model.LevelAgent, "Formulating changes and verifying codebase structure...", 75.0},
		{30 * time.Millisecond, model.LevelInfo, "Validation completed cleanly. Output verified.", 90.0},
	}

	for _, s := range steps {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.delay):
			w.handleOutputLine(s.message, false)
			w.updateProgress(s.progress)
		}
	}

	return nil
}

// handleOutputLine processes a single line emitted by the agent process.
func (w *Wrapper) handleOutputLine(line string, isStderr bool) {
	if strings.TrimSpace(line) == "" {
		return
	}

	// Classify log level
	level := ClassifyLogLevel(line, isStderr)

	// Check if line indicates progress
	if pct, ok := ParseProgress(line); ok {
		w.updateProgress(pct)
	}

	// Publish log entry
	w.publishLog(level, line)
}

func (w *Wrapper) updateProgress(pct float64) {
	w.mu.Lock()
	changed := w.job.Progress != pct
	w.job.Progress = pct
	w.job.UpdatedAt = time.Now().UTC()
	w.mu.Unlock()

	if changed {
		w.publishJobStatus()
		_ = w.writeStatusFile()
	}
}

// publishMachineStatus sends model.Machine to MQTT (QoS 1, Retained true).
func (w *Wrapper) publishMachineStatus() {
	if w.mqttClient == nil || !w.mqttClient.IsConnected() {
		return
	}
	w.mu.Lock()
	m := w.machine
	w.mu.Unlock()

	topic := fmt.Sprintf("agentpulse/v1/machines/%s/status", m.ID)
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	token := w.mqttClient.Publish(topic, 1, true, data)
	token.WaitTimeout(2 * time.Second)
}

// publishJobStatus sends model.Job to MQTT (QoS 1, Retained true).
func (w *Wrapper) publishJobStatus() {
	if w.mqttClient == nil || !w.mqttClient.IsConnected() {
		return
	}
	w.mu.Lock()
	j := w.job
	w.mu.Unlock()

	topic := fmt.Sprintf("agentpulse/v1/machines/%s/jobs/%s/status", j.MachineID, j.ID)
	data, err := json.Marshal(j)
	if err != nil {
		return
	}
	token := w.mqttClient.Publish(topic, 1, true, data)
	token.WaitTimeout(2 * time.Second)
}

// publishLog sends model.LogEntry to MQTT (QoS 0, Retained false).
func (w *Wrapper) publishLog(level model.LogLevel, message string) {
	if w.mqttClient == nil || !w.mqttClient.IsConnected() {
		return
	}
	seq := atomic.AddInt64(&w.seq, 1)

	w.mu.Lock()
	machID := w.machine.ID
	jobID := w.job.ID
	engine := w.job.Engine
	w.mu.Unlock()

	entry := struct {
		Timestamp time.Time        `json:"timestamp"`
		JobID     string           `json:"job_id"`
		MachineID string           `json:"machine_id"`
		Engine    model.EngineType `json:"engine"`
		Level     model.LogLevel   `json:"level"`
		Message   string           `json:"message"`
		Seq       int64            `json:"seq"`
	}{
		Timestamp: time.Now().UTC(),
		JobID:     jobID,
		MachineID: machID,
		Engine:    engine,
		Level:     level,
		Message:   TruncateLogMessage(message),
		Seq:       seq,
	}

	topic := fmt.Sprintf("agentpulse/v1/machines/%s/jobs/%s/logs", machID, jobID)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	w.mqttClient.Publish(topic, 0, false, data)
}

// publishAlert publishes a completion / failure notification (aligns with docs/mqtt-protocol.md §4.5).
func (w *Wrapper) publishAlert() {
	if w.mqttClient == nil || !w.mqttClient.IsConnected() {
		return
	}
	w.mu.Lock()
	j := w.job
	m := w.machine
	w.mu.Unlock()

	alert := map[string]interface{}{
		"job_id":       j.ID,
		"machine_id":   m.ID,
		"engine":       string(j.Engine),
		"status":       string(j.Status),
		"title":        j.Title,
		"cwd":          j.Cwd,
		"duration_str": j.DurationStr,
		"started_at":   j.StartedAt.Format(time.RFC3339),
		"finished_at":  j.UpdatedAt.Format(time.RFC3339),
		"error":        j.Error,
	}

	data, err := json.Marshal(alert)
	if err != nil {
		return
	}
	token := w.mqttClient.Publish("agentpulse/v1/notifications/alerts", 1, false, data)
	token.WaitTimeout(2 * time.Second)
}

// writeStatusFile atomically writes the LocalStatus snapshot to w.cfg.StatusFile.
func (w *Wrapper) writeStatusFile() error {
	if w.cfg.StatusFile == "" {
		return nil
	}

	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	w.mu.Lock()
	st := LocalStatus{
		Machine:   w.machine,
		Job:       w.job,
		PID:       w.pid,
		UpdatedAt: time.Now().UTC(),
	}
	w.mu.Unlock()

	return WriteLocalStatus(w.cfg.StatusFile, st)
}

// WriteLocalStatus atomically writes LocalStatus to disk.
func WriteLocalStatus(filePath string, status LocalStatus) error {
	dir := filepath.Dir(filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := filePath + fmt.Sprintf(".tmp.%d", time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, filePath)
}

// ReadLocalStatus reads and decodes LocalStatus from disk.
func ReadLocalStatus(filePath string) (*LocalStatus, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var st LocalStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("corrupted status file %s: %w", filePath, err)
	}
	return &st, nil
}

// ClassifyLogLevel analyzes a line and categorizes it into model.LogLevel.
func ClassifyLogLevel(line string, isStderr bool) model.LogLevel {
	lower := strings.ToLower(line)
	if isStderr {
		if strings.Contains(lower, "warn") {
			return model.LevelWarn
		}
		return model.LevelError
	}

	if strings.Contains(lower, "[agent]") || strings.Contains(lower, "thinking:") || strings.Contains(lower, "subagent") {
		return model.LevelAgent
	}
	if strings.Contains(lower, "[tool]") || strings.Contains(lower, "executing tool") || strings.Contains(lower, "run_command") || strings.HasPrefix(lower, "tool:") {
		return model.LevelTool
	}
	if strings.Contains(lower, "[error]") || strings.HasPrefix(lower, "error:") || strings.Contains(lower, "fatal:") {
		return model.LevelError
	}
	if strings.Contains(lower, "[warn]") || strings.HasPrefix(lower, "warning:") {
		return model.LevelWarn
	}
	return model.LevelInfo
}

// TruncateLogMessage truncates log lines exceeding MaxLogMessageBytes per docs/mqtt-protocol.md §6.3.
func TruncateLogMessage(msg string) string {
	if len(msg) <= MaxLogMessageBytes {
		return msg
	}
	keep := MaxLogMessageBytes - len(TruncateSuffix)
	if keep < 0 {
		keep = 0
	}
	return msg[:keep] + TruncateSuffix
}

var progressRegex = regexp.MustCompile(`(?i)(?:progress[:\s]+|[\(\[]\s*)?(\d{1,3})(?:\.\d+)?%`)

// ParseProgress searches a log line for explicit progress indicators (e.g. "Progress: 45%" or "[45%]").
func ParseProgress(line string) (float64, bool) {
	matches := progressRegex.FindStringSubmatch(line)
	if len(matches) >= 2 {
		if val, err := strconv.ParseFloat(matches[1], 64); err == nil && val >= 0 && val <= 100 {
			return val, true
		}
	}
	return 0, false
}

// lineWriter buffers stream chunks and emits completed lines.
type lineWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	onLine   func(string)
	mirrorTo io.Writer
}

func newLineWriter(onLine func(string), mirror io.Writer) *lineWriter {
	return &lineWriter{
		onLine:   onLine,
		mirrorTo: mirror,
	}
}

func (lw *lineWriter) Write(p []byte) (n int, err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	if lw.mirrorTo != nil {
		_, _ = lw.mirrorTo.Write(p)
	}

	lw.buf.Write(p)
	for {
		line, err := lw.buf.ReadString('\n')
		if err != nil {
			// Incomplete line remaining in buffer
			lw.buf.WriteString(line)
			break
		}
		clean := strings.TrimRight(line, "\r\n")
		if lw.onLine != nil {
			lw.onLine(clean)
		}
	}
	return len(p), nil
}

func (lw *lineWriter) Flush() {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	if lw.buf.Len() > 0 {
		line := strings.TrimRight(lw.buf.String(), "\r\n")
		lw.buf.Reset()
		if lw.onLine != nil && line != "" {
			lw.onLine(line)
		}
	}
}

// Helpers for node and environment detection

// DetectMachineID returns the sanitized local hostname.
// An explicit --machine flag or config.yaml machine_id is applied by the caller.
func DetectMachineID() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return SanitizeID(host)
	}
	return "local-node"
}

func detectMachineID() string {
	return DetectMachineID()
}

// SanitizeID normalizes an identifier into lowercase alphanumeric and dash characters.
func SanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	reg := regexp.MustCompile(`[^a-z0-9-]+`)
	clean := reg.ReplaceAllString(s, "-")
	regConsecutive := regexp.MustCompile(`-+`)
	clean = regConsecutive.ReplaceAllString(clean, "-")
	clean = strings.Trim(clean, "-")
	if clean == "" {
		return "local-node"
	}
	return clean
}

func sanitizeID(s string) string {
	return SanitizeID(s)
}

// DetectEngine checks command line arguments to determine if agy or opencode is being executed.
func DetectEngine(args []string) model.EngineType {
	if len(args) > 0 {
		base := strings.ToLower(filepath.Base(args[0]))
		if strings.Contains(base, "opencode") {
			return model.EngineOpenCode
		}
	}
	return model.EngineAgy
}

func detectEngine(args []string) model.EngineType {
	return DetectEngine(args)
}

// GenerateJobID generates a unique Job ID conforming to docs/mqtt-protocol.md §3.1.
func GenerateJobID(engine model.EngineType) string {
	now := time.Now().UTC()
	return fmt.Sprintf("job-%s-%s-%04x", engine, now.Format("20060102-150405"), now.Nanosecond()%0x10000)
}

func generateJobID(engine model.EngineType) string {
	return GenerateJobID(engine)
}

// GenerateCommandID generates a unique dispatch command UUID/ID.
func GenerateCommandID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		now := time.Now().UTC()
		return fmt.Sprintf("cmd-%s-%04x", now.Format("20060102-150405"), now.Nanosecond()%0x10000)
	}
	return fmt.Sprintf("cmd-%x", b)
}

// InferTitle extracts or infers a human-readable title from command arguments.
func InferTitle(args []string, command string) string {
	for i, arg := range args {
		if (arg == "-p" || arg == "--prompt" || arg == "--task") && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--prompt=") {
			return strings.TrimPrefix(arg, "--prompt=")
		}
		if strings.HasPrefix(arg, "--task=") {
			return strings.TrimPrefix(arg, "--task=")
		}
	}
	if command != "" {
		if len(command) > 60 {
			return command[:57] + "..."
		}
		return command
	}
	return "Agent pulse job"
}

func inferTitle(args []string, command string) string {
	return InferTitle(args, command)
}

// DetectLocalIP attempts to detect the local outbound IP address.
func DetectLocalIP() string {
	// UDP probe outbound routing table without sending traffic
	conn, err := net.Dial("udp", "192.0.2.1:80")
	if err == nil {
		defer conn.Close()
		localAddr := conn.LocalAddr().(*net.UDPAddr)
		return localAddr.IP.String()
	}

	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func detectLocalIP() string {
	return DetectLocalIP()
}

// BuildCommandArgs normalizes command and arguments into an argument list for execution.
func BuildCommandArgs(command string, args []string) []string {
	var parts []string
	if strings.TrimSpace(command) != "" {
		parts = strings.Fields(command)
	}
	if len(args) > 0 {
		parts = append(parts, args...)
	}
	return parts
}

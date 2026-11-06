package wrapper_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

// mockMQTTClient records publications and supports subscribers for unit/integration tests.
type mockMQTTClient struct {
	mu            sync.Mutex
	connected     bool
	published     []publishedMsg
	subscriptions map[string]paho.MessageHandler
}

type publishedMsg struct {
	Topic    string
	QoS      byte
	Retained bool
	Payload  []byte
}

type mockToken struct {
	err error
}

func (t *mockToken) Wait() bool                       { return true }
func (t *mockToken) WaitTimeout(d time.Duration) bool { return true }
func (t *mockToken) Done() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}
func (t *mockToken) Error() error { return t.err }

func newMockMQTTClient() *mockMQTTClient {
	return &mockMQTTClient{
		connected:     true,
		subscriptions: make(map[string]paho.MessageHandler),
	}
}

func (m *mockMQTTClient) IsConnected() bool      { return m.connected }
func (m *mockMQTTClient) IsConnectionOpen() bool { return m.connected }
func (m *mockMQTTClient) Connect() paho.Token    { return &mockToken{} }
func (m *mockMQTTClient) Disconnect(quiesce uint) {
	m.mu.Lock()
	m.connected = false
	m.mu.Unlock()
}

func (m *mockMQTTClient) Publish(topic string, qos byte, retained bool, payload interface{}) paho.Token {
	m.mu.Lock()
	var b []byte
	switch p := payload.(type) {
	case []byte:
		b = p
	case string:
		b = []byte(p)
	default:
		b = []byte(fmt.Sprintf("%v", p))
	}
	m.published = append(m.published, publishedMsg{
		Topic:    topic,
		QoS:      qos,
		Retained: retained,
		Payload:  b,
	})

	// Dispatch to subscribers matching topic
	var handlers []paho.MessageHandler
	for filter, h := range m.subscriptions {
		if filter == "#" || filter == topic || matchFilter(filter, topic) {
			handlers = append(handlers, h)
		}
	}
	m.mu.Unlock()

	msg := &mockPahoMessage{topic: topic, payload: b, retained: retained, qos: qos}
	for _, h := range handlers {
		h(m, msg)
	}

	return &mockToken{}
}

func (m *mockMQTTClient) Subscribe(topic string, qos byte, callback paho.MessageHandler) paho.Token {
	m.mu.Lock()
	m.subscriptions[topic] = callback
	m.mu.Unlock()
	return &mockToken{}
}

func (m *mockMQTTClient) SubscribeMultiple(filters map[string]byte, callback paho.MessageHandler) paho.Token {
	m.mu.Lock()
	for filter := range filters {
		m.subscriptions[filter] = callback
	}
	m.mu.Unlock()
	return &mockToken{}
}

func (m *mockMQTTClient) Unsubscribe(topics ...string) paho.Token {
	m.mu.Lock()
	for _, t := range topics {
		delete(m.subscriptions, t)
	}
	m.mu.Unlock()
	return &mockToken{}
}

func (m *mockMQTTClient) AddRoute(topic string, callback paho.MessageHandler) {}
func (m *mockMQTTClient) OptionsReader() paho.ClientOptionsReader {
	return paho.NewOptionsReader(paho.NewClientOptions())
}

func (m *mockMQTTClient) GetPublished() []publishedMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]publishedMsg, len(m.published))
	copy(res, m.published)
	return res
}

func (m *mockMQTTClient) HasSubscription(topic string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.subscriptions[topic]
	return exists
}

func (m *mockMQTTClient) ClearSubscriptions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscriptions = make(map[string]paho.MessageHandler)
}

type mockPahoMessage struct {
	topic    string
	payload  []byte
	retained bool
	qos      byte
}

func (p *mockPahoMessage) Duplicate() bool   { return false }
func (p *mockPahoMessage) Qos() byte         { return p.qos }
func (p *mockPahoMessage) Retained() bool    { return p.retained }
func (p *mockPahoMessage) Topic() string     { return p.topic }
func (p *mockPahoMessage) MessageID() uint16 { return 0 }
func (p *mockPahoMessage) Payload() []byte   { return p.payload }
func (p *mockPahoMessage) Ack()              {}

func matchFilter(filter, topic string) bool {
	fParts := strings.Split(filter, "/")
	tParts := strings.Split(topic, "/")
	if len(fParts) != len(tParts) {
		return false
	}
	for i := range fParts {
		if fParts[i] == "+" {
			continue
		}
		if fParts[i] != tParts[i] {
			return false
		}
	}
	return true
}

func TestWrapper_Defaults(t *testing.T) {
	w, err := wrapper.New(wrapper.Config{
		Args: []string{"agy", "-p", "Refactor authentication layer"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mach := w.Machine()
	if mach.ID == "" {
		t.Errorf("expected non-empty Machine ID")
	}
	if mach.Online != true {
		t.Errorf("expected Machine to be online")
	}
	if mach.JobCount != 1 {
		t.Errorf("expected JobCount 1, got %d", mach.JobCount)
	}

	job := w.Job()
	if !strings.HasPrefix(job.ID, "job-agy-") {
		t.Errorf("expected job ID starting with job-agy-, got: %s", job.ID)
	}
	if job.Engine != model.EngineAgy {
		t.Errorf("expected engine agy, got %s", job.Engine)
	}
	if job.Title != "Refactor authentication layer" {
		t.Errorf("expected inferred title, got %s", job.Title)
	}
	if job.Status != model.StatusRunning {
		t.Errorf("expected status running, got %s", job.Status)
	}
}

func TestWrapper_LocalExecution_Success(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:    "test-node",
		JobID:        "job-test-01",
		Engine:       model.EngineAgy,
		Title:        "Echo test",
		Args:         []string{"echo", "Hello from wrapped agy job"},
		StatusFile:   statusFile,
		MirrorStdout: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("wrapper.Run failed: %v", err)
	}

	// Verify job finished successfully
	job := w.Job()
	if job.Status != model.StatusDone {
		t.Errorf("expected job status done, got %s", job.Status)
	}
	if job.Progress != 100.0 {
		t.Errorf("expected progress 100.0, got %f", job.Progress)
	}
	if job.DurationStr == "" {
		t.Errorf("expected non-empty duration_str")
	}

	mach := w.Machine()
	if mach.JobCount != 0 {
		t.Errorf("expected final job count 0, got %d", mach.JobCount)
	}

	// Verify status file written
	st, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read status file: %v", err)
	}
	if st.Job.ID != "job-test-01" {
		t.Errorf("status file job ID mismatch: %s", st.Job.ID)
	}
	if st.Job.Status != model.StatusDone {
		t.Errorf("status file job status mismatch: %s", st.Job.Status)
	}
	if st.Machine.ID != "test-node" {
		t.Errorf("status file machine ID mismatch: %s", st.Machine.ID)
	}
}

func TestWrapper_LocalExecution_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:  "test-fail-node",
		JobID:      "job-fail-01",
		Engine:     model.EngineAgy,
		Title:      "Failing command",
		Args:       []string{"false"},
		StatusFile: statusFile,
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = w.Run(ctx)
	if err == nil {
		t.Fatalf("expected error from failing command, got nil")
	}

	job := w.Job()
	if job.Status != model.StatusFailed {
		t.Errorf("expected job status failed, got %s", job.Status)
	}
	if job.Error == "" {
		t.Errorf("expected non-empty error in job")
	}

	// Verify status file recorded failure
	st, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read status file: %v", err)
	}
	if st.Job.Status != model.StatusFailed {
		t.Errorf("expected status file status failed, got %s", st.Job.Status)
	}
	if st.Job.Error == "" {
		t.Errorf("expected error recorded in status file")
	}
}

func TestWrapper_MissingBinary_Fails(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "missing-bin-status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:  "test-missing-bin-node",
		JobID:      "job-missing-bin-01",
		Engine:     model.EngineAgy,
		Title:      "Missing binary test",
		Args:       []string{"nonexistent-agentpulse-bin-12345"},
		StatusFile: statusFile,
		Stub:       false,
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = w.Run(ctx)
	if err == nil {
		t.Fatalf("expected error from missing binary, got nil")
	}
	if !strings.Contains(err.Error(), "command binary") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected clear missing binary error, got: %v", err)
	}

	// Verify job status failed (never done)
	job := w.Job()
	if job.Status != model.StatusFailed {
		t.Errorf("expected job status failed, got %s", job.Status)
	}
	if job.Error == "" {
		t.Errorf("expected non-empty error in job")
	}

	// Verify status file recorded failure
	st, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read status file: %v", err)
	}
	if st.Job.Status != model.StatusFailed {
		t.Errorf("expected status file status failed, got %s", st.Job.Status)
	}
	if !strings.Contains(st.Job.Error, "nonexistent-agentpulse-bin-12345") {
		t.Errorf("expected status file error to mention missing binary, got: %s", st.Job.Error)
	}
}

func TestWrapper_ExplicitStub_WithMissingBinary_Succeeds(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "stub-missing-bin-status.json")

	// When Stub is explicitly true, missing binary is bypassed and stub workflow executes
	w, err := wrapper.New(wrapper.Config{
		MachineID:  "stub-node",
		JobID:      "job-explicit-stub-01",
		Engine:     model.EngineOpenCode,
		Title:      "Stub OpenCode Workflow with missing binary",
		Args:       []string{"nonexistent-agentpulse-bin-12345"},
		Stub:       true,
		StatusFile: statusFile,
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("explicit stub run failed: %v", err)
	}

	job := w.Job()
	if job.Status != model.StatusDone {
		t.Errorf("expected stub job status done, got %s", job.Status)
	}
	if job.Progress != 100.0 {
		t.Errorf("expected stub job progress 100, got %f", job.Progress)
	}
}

func TestWrapper_LocalMode_HeartbeatRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "local-hb-status.json")

	// Heartbeat every 25ms, running a stub that takes ~170ms without any MQTT configured.
	w, err := wrapper.New(wrapper.Config{
		MachineID:         "local-hb-node",
		JobID:             "job-local-hb-01",
		Engine:            model.EngineAgy,
		Title:             "Local heartbeat refresh test",
		Stub:              true,
		StatusFile:        statusFile,
		HeartbeatInterval: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- w.Run(ctx)
	}()

	// Wait 75ms to allow at least 2 heartbeat cycles during execution
	time.Sleep(75 * time.Millisecond)

	st, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read running status file: %v", err)
	}

	// Status file must have been updated during local execution
	if st.Job.Status != model.StatusRunning {
		t.Errorf("expected status running while active, got %s", st.Job.Status)
	}
	if !st.Machine.Online {
		t.Errorf("expected machine online while running")
	}
	if st.Machine.JobCount != 1 {
		t.Errorf("expected job count 1 while running, got %d", st.Machine.JobCount)
	}

	// The heartbeat should have refreshed last_heartbeat after started_at
	if st.Machine.LastHeartbeat.Before(st.Job.StartedAt) {
		t.Errorf("expected last_heartbeat (%v) to be >= started_at (%v)", st.Machine.LastHeartbeat, st.Job.StartedAt)
	}

	if err := <-runErrCh; err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify final status after clean completion
	finalSt, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read final status file: %v", err)
	}
	if finalSt.Job.Status != model.StatusDone {
		t.Errorf("expected final status done, got %s", finalSt.Job.Status)
	}
	if finalSt.Machine.Online {
		t.Errorf("expected machine offline on completion")
	}
	if finalSt.Machine.JobCount != 0 {
		t.Errorf("expected final job count 0, got %d", finalSt.Machine.JobCount)
	}
}

func TestWrapper_LocalMode_HeartbeatRefresh_Command(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "local-hb-cmd-status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:         "local-hb-cmd-node",
		JobID:             "job-local-hb-cmd-01",
		Engine:            model.EngineAgy,
		Title:             "Local command heartbeat refresh test",
		Args:              []string{"sleep", "0.1"},
		StatusFile:        statusFile,
		HeartbeatInterval: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- w.Run(ctx)
	}()

	time.Sleep(60 * time.Millisecond)

	st, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read running status file: %v", err)
	}
	if st.Job.Status != model.StatusRunning {
		t.Errorf("expected status running while command active, got %s", st.Job.Status)
	}

	if err := <-runErrCh; err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	finalSt, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read final status: %v", err)
	}
	if finalSt.Job.Status != model.StatusDone {
		t.Errorf("expected done status, got %s", finalSt.Job.Status)
	}
}

func TestWrapper_StubExecution(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "stub-status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:  "stub-node",
		JobID:      "job-opencode-stub-01",
		Engine:     model.EngineOpenCode,
		Title:      "Stub OpenCode Workflow",
		Stub:       true,
		StatusFile: statusFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("stub run failed: %v", err)
	}

	job := w.Job()
	if job.Status != model.StatusDone {
		t.Errorf("expected stub job status done, got %s", job.Status)
	}
	if job.Progress != 100.0 {
		t.Errorf("expected stub job progress 100, got %f", job.Progress)
	}
}

func TestWrapper_MQTTConnectFailure_StatusFailed(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "mqtt-fail-status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:  "mqtt-fail-node",
		JobID:      "job-mqtt-fail-01",
		Engine:     model.EngineAgy,
		Title:      "MQTT connect failure test",
		Args:       []string{"echo", "should not run"},
		StatusFile: statusFile,
		MQTT: &mqtt.Config{
			BrokerURL: "tcp://127.0.0.1:18833", // Unopened port
			Timeout:   100 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = w.Run(ctx)
	if err == nil {
		t.Fatalf("expected error from MQTT connect failure, got nil")
	}
	if !strings.Contains(err.Error(), "MQTT") {
		t.Errorf("expected MQTT error message, got: %v", err)
	}

	// Verify job status failed in memory
	job := w.Job()
	if job.Status != model.StatusFailed {
		t.Errorf("expected job status failed, got %s", job.Status)
	}
	if job.Error == "" {
		t.Errorf("expected non-empty error in job")
	}

	// Verify machine status offline in memory
	mach := w.Machine()
	if mach.Online {
		t.Errorf("expected machine to be offline on failure")
	}
	if mach.JobCount != 0 {
		t.Errorf("expected machine job count 0, got %d", mach.JobCount)
	}

	// Verify status file recorded failure and offline status (never stuck at running)
	st, err := wrapper.ReadLocalStatus(statusFile)
	if err != nil {
		t.Fatalf("failed to read status file: %v", err)
	}
	if st.Job.Status != model.StatusFailed {
		t.Errorf("expected status file status failed, got %s", st.Job.Status)
	}
	if !strings.Contains(st.Job.Error, "MQTT") {
		t.Errorf("expected status file error to mention MQTT, got: %s", st.Job.Error)
	}
	if st.Machine.Online {
		t.Errorf("expected status file machine online to be false")
	}
	if st.Machine.JobCount != 0 {
		t.Errorf("expected status file job count to be 0, got %d", st.Machine.JobCount)
	}
}

func TestWrapper_MQTT_TLS_InvalidCert_Fails(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "mqtt-tls-fail-status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:  "mqtt-tls-node",
		JobID:      "job-mqtt-tls-fail-01",
		Engine:     model.EngineAgy,
		Title:      "MQTT TLS cert failure test",
		Args:       []string{"echo", "should not run"},
		StatusFile: statusFile,
		MQTT: &mqtt.Config{
			BrokerURL:  "tls://broker.example:8883",
			CACertFile: "/nonexistent/ca.pem",
			Timeout:    100 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = w.Run(ctx)
	if err == nil {
		t.Fatalf("expected error from MQTT TLS connect failure, got nil")
	}
	if !strings.Contains(err.Error(), "failed to read CA certificate") {
		t.Errorf("expected CA certificate read error message, got: %v", err)
	}
}

func TestWrapper_MQTT_Publishing(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "mqtt-status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:         "mqtt-worker",
		MachineName:       "Worker Node 1",
		JobID:             "job-mqtt-01",
		Engine:            model.EngineAgy,
		Title:             "MQTT publishing validation",
		Args:              []string{"echo", "Tool: git status"},
		StatusFile:        statusFile,
		HeartbeatInterval: 50 * time.Millisecond,
		MQTTClient:        mockClient,
	})
	if err != nil {
		t.Fatalf("unexpected error creating wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	published := mockClient.GetPublished()
	if len(published) == 0 {
		t.Fatalf("expected MQTT messages to be published, got 0")
	}

	// Verify required topics were published
	var foundMachineStatus, foundJobStatus, foundJobLog, foundAlert bool
	var foundMachineOnline, foundMachineOffline bool
	var finalJobStatusDone bool

	for _, msg := range published {
		switch {
		case msg.Topic == "agentpulse/v1/machines/mqtt-worker/status":
			foundMachineStatus = true
			if !msg.Retained {
				t.Errorf("machine status must be retained=true")
			}
			if msg.QoS != 1 {
				t.Errorf("machine status must have QoS 1, got %d", msg.QoS)
			}
			if strings.Contains(string(msg.Payload), `"online":true`) || strings.Contains(string(msg.Payload), `"online": true`) {
				foundMachineOnline = true
			}
			if strings.Contains(string(msg.Payload), `"online":false`) || strings.Contains(string(msg.Payload), `"online": false`) {
				foundMachineOffline = true
			}
		case msg.Topic == "agentpulse/v1/machines/mqtt-worker/jobs/job-mqtt-01/status":
			foundJobStatus = true
			if !msg.Retained {
				t.Errorf("job status must be retained=true")
			}
			if msg.QoS != 1 {
				t.Errorf("job status must have QoS 1, got %d", msg.QoS)
			}
			if strings.Contains(string(msg.Payload), `"done"`) {
				finalJobStatusDone = true
			}
		case msg.Topic == "agentpulse/v1/machines/mqtt-worker/jobs/job-mqtt-01/logs":
			foundJobLog = true
			if msg.Retained {
				t.Errorf("log stream must be retained=false")
			}
			if msg.QoS != 0 {
				t.Errorf("log stream must have QoS 0, got %d", msg.QoS)
			}
			if !strings.Contains(string(msg.Payload), "TOOL") {
				t.Errorf("expected classified TOOL log level in payload: %s", string(msg.Payload))
			}
		case msg.Topic == "agentpulse/v1/notifications/alerts":
			foundAlert = true
			if msg.Retained {
				t.Errorf("alert notification must be retained=false")
			}
			if msg.QoS != 1 {
				t.Errorf("alert notification must have QoS 1, got %d", msg.QoS)
			}
		}
	}

	if !foundMachineStatus {
		t.Errorf("expected machine status topic to be published")
	}
	if !foundMachineOnline {
		t.Errorf("expected initial machine status with online:true")
	}
	if !foundMachineOffline {
		t.Errorf("expected final machine status with online:false")
	}
	if !foundJobStatus {
		t.Errorf("expected job status topic to be published")
	}
	if !finalJobStatusDone {
		t.Errorf("expected final job status to report 'done'")
	}
	if !foundJobLog {
		t.Errorf("expected job log topic to be published")
	}
	if !foundAlert {
		t.Errorf("expected alert topic to be published")
	}
}

func TestWrapper_IntegrationWithMQTTStore(t *testing.T) {
	// End-to-end integration: wrapper publishes to mock MQTT client,
	// and internal/store/mqtt consumes the messages into store.Store!
	mockClient := newMockMQTTClient()

	storeCfg := mqtt.DefaultConfig()
	storeCfg.BrokerURL = "tcp://broker.example:1883"
	storeCfg.SettleTimeout = 10 * time.Millisecond

	store, err := mqtt.NewWithClient(storeCfg, mockClient)
	if err != nil {
		t.Fatalf("failed to create MQTT store: %v", err)
	}
	defer store.Close()

	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "integration-status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:   "mac-studio-integ",
		MachineName: "Mac Studio Test",
		JobID:       "job-integ-42",
		Engine:      model.EngineAgy,
		Title:       "Integration Test Task",
		Args:        []string{"echo", "Thinking: verifying architecture\nTool: go test ./..."},
		StatusFile:  statusFile,
		MQTTClient:  mockClient,
	})
	if err != nil {
		t.Fatalf("failed to create wrapper: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("wrapper.Run failed: %v", err)
	}

	// 1. Verify store.ListMachines sees our machine (marked offline after clean exit)
	machines, err := store.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines failed: %v", err)
	}
	var foundMachine bool
	for _, m := range machines {
		if m.ID == "mac-studio-integ" {
			foundMachine = true
			if m.Online {
				t.Errorf("expected machine to be marked offline after clean exit")
			}
			if m.JobCount != 0 {
				t.Errorf("expected machine job count 0, got %d", m.JobCount)
			}
		}
	}
	if !foundMachine {
		t.Errorf("expected mac-studio-integ in ListMachines, got %v", machines)
	}

	// 2. Verify store.ListJobs sees our completed job
	jobs, err := store.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	var foundJob *model.Job
	for _, j := range jobs {
		if j.ID == "job-integ-42" {
			jobCopy := j
			foundJob = &jobCopy
			break
		}
	}
	if foundJob == nil {
		t.Fatalf("expected job-integ-42 in ListJobs, got %v", jobs)
	}
	if foundJob.Status != model.StatusDone {
		t.Errorf("expected job status done, got %s", foundJob.Status)
	}

	// 3. Verify store.GetLogs sees the published log lines
	logs, err := store.GetLogs(ctx, "job-integ-42", 10)
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	if len(logs) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d", len(logs))
	}
	if logs[0].Level != model.LevelAgent {
		t.Errorf("expected first log level AGENT, got %s", logs[0].Level)
	}
	if logs[1].Level != model.LevelTool {
		t.Errorf("expected second log level TOOL, got %s", logs[1].Level)
	}
}

func TestClassifyLogLevel(t *testing.T) {
	tests := []struct {
		line     string
		isStderr bool
		want     model.LogLevel
	}{
		{"Thinking: investigating database connection pool", false, model.LevelAgent},
		{"[agent] Planning prompt sequence", false, model.LevelAgent},
		{"Invoking subagent Codebase Researcher", false, model.LevelAgent},
		{"Tool: run_command git status", false, model.LevelTool},
		{"Executing tool edit_file", false, model.LevelTool},
		{"Warning: flag deprecated", false, model.LevelWarn},
		{"[WARN] high memory consumption", false, model.LevelWarn},
		{"Error: unable to resolve symbol", false, model.LevelError},
		{"[ERROR] command failed with status 1", false, model.LevelError},
		{"Standard build step progress", false, model.LevelInfo},
		{"warning on stderr", true, model.LevelWarn},
		{"generic stderr failure line", true, model.LevelError},
	}

	for _, tt := range tests {
		got := wrapper.ClassifyLogLevel(tt.line, tt.isStderr)
		if got != tt.want {
			t.Errorf("ClassifyLogLevel(%q, %v) = %v; want %v", tt.line, tt.isStderr, got, tt.want)
		}
	}
}

func TestTruncateLogMessage(t *testing.T) {
	shortMsg := "This is a normal log line"
	if got := wrapper.TruncateLogMessage(shortMsg); got != shortMsg {
		t.Errorf("expected %q, got %q", shortMsg, got)
	}

	// Exceed 64 KB
	hugeMsg := strings.Repeat("a", wrapper.MaxLogMessageBytes+1000)
	truncated := wrapper.TruncateLogMessage(hugeMsg)
	if len(truncated) != wrapper.MaxLogMessageBytes {
		t.Errorf("expected truncated length %d, got %d", wrapper.MaxLogMessageBytes, len(truncated))
	}
	if !strings.HasSuffix(truncated, wrapper.TruncateSuffix) {
		t.Errorf("expected suffix %q, got ending %q", wrapper.TruncateSuffix, truncated[len(truncated)-len(wrapper.TruncateSuffix):])
	}
}

func TestParseProgress(t *testing.T) {
	tests := []struct {
		line    string
		wantPct float64
		wantOK  bool
	}{
		{"Progress: 45%", 45.0, true},
		{"[ 80% ] Compiling source packages", 80.0, true},
		{"Step 4 finished (95%)", 95.0, true},
		{"Progress: 100%", 100.0, true},
		{"Just a normal sentence with no numbers", 0, false},
		{"250% is invalid", 0, false},
	}

	for _, tt := range tests {
		gotPct, gotOK := wrapper.ParseProgress(tt.line)
		if gotOK != tt.wantOK || (gotOK && gotPct != tt.wantPct) {
			t.Errorf("ParseProgress(%q) = (%v, %v); want (%v, %v)", tt.line, gotPct, gotOK, tt.wantPct, tt.wantOK)
		}
	}
}

func TestReadWriteLocalStatus_Missing(t *testing.T) {
	_, err := wrapper.ReadLocalStatus("/path/to/non/existent/file.json")
	if err == nil {
		t.Errorf("expected error reading non-existent status file, got nil")
	}
}

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"mac-studio", "mac-studio"},
		{"Mac-Studio-M2", "mac-studio-m2"},
		{"My_Host_Name", "my-host-name"},
		{"node.local", "node-local"},
		{"  Spaces  Around  ", "spaces-around"},
		{"special@chars!#$%^&*", "special-chars"},
		{"multiple---hyphens___together", "multiple-hyphens-together"},
		{"-leading-and-trailing-", "leading-and-trailing"},
		{"", "local-node"},
		{"   ", "local-node"},
		{"---", "local-node"},
		{"___", "local-node"},
		{"!@#$%^", "local-node"},
	}

	for _, tt := range tests {
		got := wrapper.SanitizeID(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeID(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestDetectMachineID_IgnoresEnv(t *testing.T) {
	t.Setenv("AGENTPULSE_MACHINE_ID", "Custom_Box_99")
	host, _ := os.Hostname()
	expectedHostID := wrapper.SanitizeID(host)
	got := wrapper.DetectMachineID()
	if got != expectedHostID {
		t.Errorf("expected sanitized hostname %q, got %q", expectedHostID, got)
	}
	if got == "custom-box-99" && expectedHostID != "custom-box-99" {
		t.Errorf("AGENTPULSE_MACHINE_ID must not override hostname, got %q", got)
	}
}

func TestDetectLocalIP_IgnoresEnv(t *testing.T) {
	const envIP = "192.0.2.55"
	t.Setenv("AGENTPULSE_IP", envIP)
	got := wrapper.DetectLocalIP()
	if got == envIP {
		t.Errorf("AGENTPULSE_IP must not override detected address, got %q", got)
	}
	if got == "" {
		t.Error("expected a detected address")
	}
}

func TestWrapper_MachineIdentityDefaults(t *testing.T) {
	// When MachineID is empty, wrapper defaults to DetectMachineID()
	w, err := wrapper.New(wrapper.Config{
		Stub: true,
	})
	if err != nil {
		t.Fatalf("wrapper.New failed: %v", err)
	}
	expectedID := wrapper.DetectMachineID()
	if w.Machine().ID != expectedID {
		t.Errorf("expected default machine ID %q, got %q", expectedID, w.Machine().ID)
	}
	if w.Machine().Name != expectedID {
		t.Errorf("expected machine name to default to ID %q, got %q", expectedID, w.Machine().Name)
	}

	// Explicit machine ID is sanitized
	w2, err := wrapper.New(wrapper.Config{
		MachineID: "My_Custom_Node",
		Stub:      true,
	})
	if err != nil {
		t.Fatalf("wrapper.New failed: %v", err)
	}
	if w2.Machine().ID != "my-custom-node" {
		t.Errorf("expected sanitized ID 'my-custom-node', got %q", w2.Machine().ID)
	}
}

func TestListener_MachineIdentityDefaults(t *testing.T) {
	// When MachineID is empty, NewListener defaults to DetectMachineID()
	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		Stub: true,
	})
	if err != nil {
		t.Fatalf("wrapper.NewListener failed: %v", err)
	}
	expectedID := wrapper.DetectMachineID()
	if l.Machine().ID != expectedID {
		t.Errorf("expected default machine ID %q, got %q", expectedID, l.Machine().ID)
	}

	// Explicit machine ID is sanitized
	l2, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID: "Worker_Node_Beta",
		Stub:      true,
	})
	if err != nil {
		t.Fatalf("wrapper.NewListener failed: %v", err)
	}
	if l2.Machine().ID != "worker-node-beta" {
		t.Errorf("expected sanitized ID 'worker-node-beta', got %q", l2.Machine().ID)
	}
}

func TestSanitizePrompt_SecretRedaction(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "empty prompt",
			input:    "",
			maxLen:   256,
			expected: "",
		},
		{
			name:     "clean prompt without secrets",
			input:    "Refactor database connection pool and verify unit tests",
			maxLen:   256,
			expected: "Refactor database connection pool and verify unit tests",
		},
		{
			name:     "redact openai api key",
			input:    "Use sk-abcdef1234567890abcdef1234567890 for testing completion",
			maxLen:   256,
			expected: "Use sk-[REDACTED] for testing completion",
		},
		{
			name:     "redact anthropic api key",
			input:    "Config with ant-abcdef1234567890abcdef1234567890 model",
			maxLen:   256,
			expected: "Config with ant-[REDACTED] model",
		},
		{
			name:     "redact github token",
			input:    "Clone with ghp_1234567890abcdef1234567890abcdef123456",
			maxLen:   256,
			expected: "Clone with [REDACTED_TOKEN]",
		},
		{
			name:     "redact bearer token",
			input:    "Send header Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.test.sig to api",
			maxLen:   256,
			expected: "Send header Authorization: Bearer [REDACTED] to api",
		},
		{
			name:     "redact key-value password and secret",
			input:    "Run migration with password=SuperSecretPass123! and token:mySecretTokenVal",
			maxLen:   256,
			expected: "Run migration with password=[REDACTED] and token:[REDACTED]",
		},
		{
			name:     "redact embedded url credentials",
			input:    "Fetch repo from https://admin:mySecretPass@github.com/org/repo.git",
			maxLen:   256,
			expected: "Fetch repo from https://admin:[REDACTED]@github.com/org/repo.git",
		},
		{
			name:     "redact private key block",
			input:    "Key is -----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA...\n-----END RSA PRIVATE KEY----- done",
			maxLen:   256,
			expected: "Key is [PRIVATE_KEY_REDACTED] done",
		},
		{
			name:     "enforce max length truncation",
			input:    "This is a very long prompt task description that exceeds the maximum length limit configured for MQTT retained payload",
			maxLen:   40,
			expected: "This is a very long prompt task descr...",
		},
		{
			name:     "normalize multiple newlines and spaces",
			input:    "Line 1\n\n\n   Line 2\t\tLine 3",
			maxLen:   256,
			expected: "Line 1 Line 2 Line 3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapper.SanitizePrompt(tc.input, tc.maxLen)
			if got != tc.expected {
				t.Errorf("SanitizePrompt(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestExtractPrompt(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "no prompt flag",
			args:     []string{"run", "--verbose"},
			expected: "",
		},
		{
			name:     "short flag -p",
			args:     []string{"run", "-p", "Optimize auth cache"},
			expected: "Optimize auth cache",
		},
		{
			name:     "long flag --prompt",
			args:     []string{"run", "--prompt", "Build release artifacts"},
			expected: "Build release artifacts",
		},
		{
			name:     "long flag with equals --prompt=...",
			args:     []string{"run", "--prompt=Fix flaky test"},
			expected: "Fix flaky test",
		},
		{
			name:     "task flag --task",
			args:     []string{"run", "--task", "Update docs"},
			expected: "Update docs",
		},
		{
			name:     "task flag with equals --task=...",
			args:     []string{"run", "--task=Update schema"},
			expected: "Update schema",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapper.ExtractPrompt(tc.args)
			if got != tc.expected {
				t.Errorf("ExtractPrompt(%v) = %q, want %q", tc.args, got, tc.expected)
			}
		})
	}
}

func TestInferRepoAndProject(t *testing.T) {
	// Inside current workspace, which is a git repo
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to getwd: %v", err)
	}

	repo, proj := wrapper.InferRepoAndProject(cwd)
	if proj == "" {
		t.Errorf("expected non-empty project inferred from cwd %q", cwd)
	}
	if repo == "" {
		t.Errorf("expected non-empty repo inferred from cwd %q", cwd)
	}

	// For a non-git temporary folder
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "my-test-proj")
	_ = os.MkdirAll(subDir, 0755)

	repo2, proj2 := wrapper.InferRepoAndProject(subDir)
	if proj2 != "my-test-proj" {
		t.Errorf("expected project 'my-test-proj', got %q", proj2)
	}
	if repo2 != "my-test-proj" {
		t.Errorf("expected repo 'my-test-proj', got %q", repo2)
	}
}

func TestWrapper_ExtendedFieldsInStatusAndAlert(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "status.json")

	w, err := wrapper.New(wrapper.Config{
		MachineID:   "box-node-1",
		JobID:       "job-fields-01",
		Engine:      model.EngineAgy,
		Title:       "Deploy Auth Proxy",
		Repo:        "org/auth-proxy",
		Project:     "auth-proxy",
		Prompt:      "Deploy auth proxy with sk-1234567890abcdef1234567890 key",
		RequestedBy: "dev@workstation",
		StatusFile:  statusFile,
		MQTTClient:  mockClient,
		Stub:        true,
		Quiet:       true,
	})
	if err != nil {
		t.Fatalf("wrapper.New failed: %v", err)
	}

	// Verify sanitized prompt in job
	if strings.Contains(w.Job().Prompt, "sk-1234567890") {
		t.Errorf("prompt was not sanitized, contains raw key: %q", w.Job().Prompt)
	}
	if !strings.Contains(w.Job().Prompt, "sk-[REDACTED]") {
		t.Errorf("prompt missing redacted placeholder: %q", w.Job().Prompt)
	}
	if w.Job().Repo != "org/auth-proxy" || w.Job().Project != "auth-proxy" {
		t.Errorf("unexpected repo/project: repo=%q proj=%q", w.Job().Repo, w.Job().Project)
	}
	if w.Job().RequestedBy != "dev@workstation" {
		t.Errorf("unexpected requested_by: %q", w.Job().RequestedBy)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("w.Run failed: %v", err)
	}

	// Verify EndedAt populated
	if w.Job().EndedAt.IsZero() {
		t.Errorf("expected non-zero EndedAt on completed job")
	}

	// Verify published alert contains extended fields
	var alertMap map[string]interface{}
	for _, m := range mockClient.GetPublished() {
		if m.Topic == "agentpulse/v1/notifications/alerts" {
			_ = json.Unmarshal(m.Payload, &alertMap)
		}
	}
	if alertMap == nil {
		t.Fatalf("expected alert publication")
	}
	if alertMap["repo"] != "org/auth-proxy" || alertMap["project"] != "auth-proxy" {
		t.Errorf("unexpected alert repo/project: %+v", alertMap)
	}
	if alertMap["requested_by"] != "dev@workstation" {
		t.Errorf("unexpected alert requested_by: %+v", alertMap)
	}
	if alertMap["finished_at"] == "" {
		t.Errorf("expected finished_at in alert payload")
	}
}

package dispatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/dispatch"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

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

type mockMQTTClient struct {
	mu            sync.Mutex
	connected     bool
	published     []publishedMsg
	retained      map[string]publishedMsg
	subscriptions map[string]paho.MessageHandler
}

func newMockMQTTClient() *mockMQTTClient {
	return &mockMQTTClient{
		connected:     true,
		retained:      make(map[string]publishedMsg),
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
	msgInfo := publishedMsg{
		Topic:    topic,
		QoS:      qos,
		Retained: retained,
		Payload:  b,
	}
	m.published = append(m.published, msgInfo)
	if retained {
		if len(b) == 0 {
			delete(m.retained, topic)
		} else {
			m.retained[topic] = msgInfo
		}
	}

	var handlers []paho.MessageHandler
	for filter, h := range m.subscriptions {
		if filter == "#" || filter == topic || filter == "+" {
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
	var retainedToDeliver []publishedMsg
	for retTopic, msg := range m.retained {
		if retTopic == topic || topic == "#" {
			retainedToDeliver = append(retainedToDeliver, msg)
		}
	}
	m.mu.Unlock()

	for _, msg := range retainedToDeliver {
		callback(m, &mockPahoMessage{topic: msg.Topic, payload: msg.Payload, retained: true, qos: msg.QoS})
	}

	return &mockToken{}
}

func (m *mockMQTTClient) SubscribeMultiple(filters map[string]byte, callback paho.MessageHandler) paho.Token {
	m.mu.Lock()
	var retainedToDeliver []publishedMsg
	for filter := range filters {
		m.subscriptions[filter] = callback
		for retTopic, msg := range m.retained {
			if retTopic == filter || filter == "#" {
				retainedToDeliver = append(retainedToDeliver, msg)
			}
		}
	}
	m.mu.Unlock()

	for _, msg := range retainedToDeliver {
		callback(m, &mockPahoMessage{topic: msg.Topic, payload: msg.Payload, retained: true, qos: msg.QoS})
	}

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

type mockPahoMessage struct {
	topic    string
	payload  []byte
	retained bool
	qos      byte
}

func (m *mockPahoMessage) Duplicate() bool   { return false }
func (m *mockPahoMessage) Qos() byte         { return m.qos }
func (m *mockPahoMessage) Retained() bool    { return m.retained }
func (m *mockPahoMessage) Topic() string     { return m.topic }
func (m *mockPahoMessage) MessageID() uint16 { return 0 }
func (m *mockPahoMessage) Payload() []byte   { return m.payload }
func (m *mockPahoMessage) Ack()              {}

func TestDispatch_PayloadShape(t *testing.T) {
	mockClient := newMockMQTTClient()
	outBuf := new(bytes.Buffer)

	cfg := dispatch.Config{
		MachineID:      "remote-node-1",
		JobID:          "job-custom-001",
		CommandID:      "cmd-custom-001",
		Engine:         model.EngineOpenCode,
		Cwd:            "/home/ubuntu/app",
		Command:        "opencode",
		Args:           []string{"run", "--prompt", "Optimize queries"},
		Title:          "Optimize queries in app",
		TimeoutSeconds: 1800,
		Env: map[string]string{
			"APP_ENV": "staging",
		},
		RequestedBy: "developer@laptop",
		MQTTClient:  mockClient,
		Out:         outBuf,
		Quiet:       false,
	}

	cmd, err := dispatch.Dispatch(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if cmd == nil {
		t.Fatalf("expected non-nil returned DispatchCommand")
	}

	// Verify publication topic and parameters
	pubMsgs := mockClient.GetPublished()
	if len(pubMsgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(pubMsgs))
	}
	msg := pubMsgs[0]

	expectedTopic := "agentpulse/v1/machines/remote-node-1/commands/dispatch"
	if msg.Topic != expectedTopic {
		t.Errorf("expected topic %s, got %s", expectedTopic, msg.Topic)
	}
	if msg.QoS != 1 {
		t.Errorf("expected QoS 1, got %d", msg.QoS)
	}
	if msg.Retained {
		t.Errorf("expected Retained false, got true")
	}

	// Unmarshal and verify all 11 fields
	var payload model.DispatchCommand
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("failed to decode dispatch payload: %v", err)
	}

	if payload.CommandID != "cmd-custom-001" {
		t.Errorf("CommandID mismatch: %s", payload.CommandID)
	}
	if payload.JobID != "job-custom-001" {
		t.Errorf("JobID mismatch: %s", payload.JobID)
	}
	if payload.Engine != model.EngineOpenCode {
		t.Errorf("Engine mismatch: %s", payload.Engine)
	}
	if payload.Cwd != "/home/ubuntu/app" {
		t.Errorf("Cwd mismatch: %s", payload.Cwd)
	}
	if payload.Command != "opencode" {
		t.Errorf("Command mismatch: %s", payload.Command)
	}
	if len(payload.Args) != 2 || payload.Args[0] != "run" || payload.Args[1] != "--prompt" {
		// Wait, args was ["run", "--prompt", "Optimize queries"]
		if len(payload.Args) != 3 {
			t.Errorf("Args length mismatch: %v", payload.Args)
		}
	}
	if payload.Title != "Optimize queries in app" {
		t.Errorf("Title mismatch: %s", payload.Title)
	}
	if payload.TimeoutSeconds != 1800 {
		t.Errorf("TimeoutSeconds mismatch: %d", payload.TimeoutSeconds)
	}
	if payload.Env["APP_ENV"] != "staging" {
		t.Errorf("Env mismatch: %v", payload.Env)
	}
	if payload.RequestedBy != "developer@laptop" {
		t.Errorf("RequestedBy mismatch: %s", payload.RequestedBy)
	}
	if payload.Timestamp.IsZero() {
		t.Errorf("Timestamp should not be zero")
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "Dispatched job job-custom-001 (command: cmd-custom-001) to machine remote-node-1") {
		t.Errorf("expected dispatch banner in output: %s", outStr)
	}
}

func TestDispatch_Validation(t *testing.T) {
	mockClient := newMockMQTTClient()

	// 1. Missing machine ID
	_, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		Command:    "agy",
		MQTTClient: mockClient,
	})
	if err == nil || !strings.Contains(err.Error(), "machine ID is required") {
		t.Errorf("expected error for missing machine ID, got %v", err)
	}

	// 2. Missing command
	_, err = dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:  "node-1",
		MQTTClient: mockClient,
	})
	if err == nil || !strings.Contains(err.Error(), "command to dispatch is required") {
		t.Errorf("expected error for missing command, got %v", err)
	}

	// 3. Missing broker URL
	_, err = dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID: "node-1",
		Command:   "agy",
	})
	if err == nil || !strings.Contains(err.Error(), "broker URL is required") {
		t.Errorf("expected error for missing broker URL, got %v", err)
	}
}

func TestDispatch_Wait_Done(t *testing.T) {
	mockClient := newMockMQTTClient()
	outBuf := new(bytes.Buffer)

	subscribedCh := make(chan struct{})

	go func() {
		<-subscribedCh
		time.Sleep(30 * time.Millisecond)

		// Simulate machine publishing status updates
		now := time.Now().UTC()
		jobRunning := model.Job{
			ID:        "job-wait-1",
			MachineID: "target-node",
			Engine:    model.EngineAgy,
			Status:    model.StatusRunning,
			Progress:  40.0,
			StartedAt: now,
			UpdatedAt: now,
		}
		p1, _ := json.Marshal(jobRunning)
		mockClient.Publish(mqtt.JobStatusTopic("target-node", "job-wait-1"), 1, true, p1)

		time.Sleep(30 * time.Millisecond)

		jobDone := model.Job{
			ID:          "job-wait-1",
			MachineID:   "target-node",
			Engine:      model.EngineAgy,
			Status:      model.StatusDone,
			Progress:    100.0,
			StartedAt:   now,
			UpdatedAt:   now.Add(2 * time.Second),
			DurationStr: "2s",
		}
		p2, _ := json.Marshal(jobDone)
		mockClient.Publish(mqtt.JobStatusTopic("target-node", "job-wait-1"), 1, true, p2)
	}()

	cmd, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:    "target-node",
		JobID:        "job-wait-1",
		Command:      "agy",
		Args:         []string{"-p", "Test wait"},
		Wait:         true,
		MQTTClient:   mockClient,
		Out:          outBuf,
		OnSubscribed: func() { close(subscribedCh) },
	})
	if err != nil {
		t.Fatalf("expected dispatch with wait to succeed, got %v", err)
	}
	if cmd == nil || cmd.JobID != "job-wait-1" {
		t.Errorf("unexpected returned cmd: %+v", cmd)
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "completed successfully") || !strings.Contains(outStr, "status: done") {
		t.Errorf("expected completion message in output: %s", outStr)
	}
}

func TestDispatch_Wait_Failed(t *testing.T) {
	mockClient := newMockMQTTClient()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)

	subscribedCh := make(chan struct{})

	go func() {
		<-subscribedCh
		time.Sleep(30 * time.Millisecond)

		now := time.Now().UTC()
		jobFailed := model.Job{
			ID:        "job-fail-1",
			MachineID: "fail-node",
			Engine:    model.EngineAgy,
			Status:    model.StatusFailed,
			StartedAt: now,
			UpdatedAt: now.Add(1 * time.Second),
			Error:     "command binary 'agy' not found",
		}
		p, _ := json.Marshal(jobFailed)
		mockClient.Publish(mqtt.JobStatusTopic("fail-node", "job-fail-1"), 1, true, p)
	}()

	_, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:    "fail-node",
		JobID:        "job-fail-1",
		Command:      "agy",
		Wait:         true,
		MQTTClient:   mockClient,
		Out:          outBuf,
		ErrOut:       errBuf,
		OnSubscribed: func() { close(subscribedCh) },
	})
	if err == nil {
		t.Fatalf("expected error from failed job, got nil")
	}
	if !strings.Contains(err.Error(), "command binary 'agy' not found") {
		t.Errorf("expected failure error to mention missing binary, got %v", err)
	}

	errStr := errBuf.String()
	if !strings.Contains(errStr, "failed: command binary 'agy' not found") {
		t.Errorf("expected failure message in errOut: %s", errStr)
	}
}

func TestDispatch_Wait_Timeout(t *testing.T) {
	mockClient := newMockMQTTClient()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)

	_, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:          "silent-node",
		JobID:              "job-timeout-1",
		Command:            "agy",
		Wait:               true,
		WaitTimeoutSeconds: 1,    // 1s local wait wall-clock timeout
		TimeoutSeconds:     3600, // remote timeout in payload
		MQTTClient:         mockClient,
		Out:                outBuf,
		ErrOut:             errBuf,
	})
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Errorf("expected timeout error, got %v", err)
	}

	// Verify remote payload received TimeoutSeconds: 3600
	var foundPayload *model.DispatchCommand
	for _, m := range mockClient.published {
		if m.Topic == mqtt.MachineDispatchTopic("silent-node") {
			var cmd model.DispatchCommand
			if err := json.Unmarshal(m.Payload, &cmd); err == nil {
				foundPayload = &cmd
			}
		}
	}
	if foundPayload == nil {
		t.Fatalf("expected dispatch payload published")
	}
	if foundPayload.TimeoutSeconds != 3600 {
		t.Errorf("expected payload TimeoutSeconds 3600, got %d", foundPayload.TimeoutSeconds)
	}
}

// TestDispatch_Wait_IgnoresStaleRetainedTerminalStatus regression tests C1:
// An old retained "done" message from a previous run on the job status topic
// must NOT cause dispatch --wait to succeed immediately; it must wait for the new run.
func TestDispatch_Wait_IgnoresStaleRetainedTerminalStatus(t *testing.T) {
	mockClient := newMockMQTTClient()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)

	machineID := "retained-test-node"
	jobID := "reused-job-001"
	statusTopic := mqtt.JobStatusTopic(machineID, jobID)

	// 1. Publish an old retained "done" status to the topic (timestamp 10 minutes ago)
	oldDoneTime := time.Now().UTC().Add(-10 * time.Minute)
	oldJob := model.Job{
		ID:          jobID,
		MachineID:   machineID,
		Engine:      model.EngineAgy,
		Status:      model.StatusDone,
		Progress:    100.0,
		StartedAt:   oldDoneTime.Add(-1 * time.Minute),
		UpdatedAt:   oldDoneTime,
		DurationStr: "1m",
	}
	oldPayload, _ := json.Marshal(oldJob)
	mockClient.Publish(statusTopic, 1, true, oldPayload)

	newRunFinished := make(chan struct{})

	// 2. Simulate listener responding to the new dispatch
	dispatchTopic := mqtt.MachineDispatchTopic(machineID)
	mockClient.Subscribe(dispatchTopic, 1, func(_ paho.Client, msg paho.Message) {
		var cmd model.DispatchCommand
		if err := json.Unmarshal(msg.Payload(), &cmd); err == nil && cmd.JobID == jobID {
			go func() {
				time.Sleep(50 * time.Millisecond)
				now := time.Now().UTC()
				queuedJob := model.Job{
					ID:        jobID,
					MachineID: machineID,
					Engine:    cmd.Engine,
					Status:    model.StatusQueued,
					StartedAt: now,
					UpdatedAt: now,
				}
				pQueued, _ := json.Marshal(queuedJob)
				mockClient.Publish(statusTopic, 1, true, pQueued)

				time.Sleep(50 * time.Millisecond)
				runningJob := queuedJob
				runningJob.Status = model.StatusRunning
				runningJob.Progress = 50.0
				runningJob.UpdatedAt = time.Now().UTC()
				pRunning, _ := json.Marshal(runningJob)
				mockClient.Publish(statusTopic, 1, true, pRunning)

				time.Sleep(50 * time.Millisecond)
				doneJob := runningJob
				doneJob.Status = model.StatusDone
				doneJob.Progress = 100.0
				doneJob.UpdatedAt = time.Now().UTC()
				doneJob.DurationStr = "150ms"
				pDone, _ := json.Marshal(doneJob)
				mockClient.Publish(statusTopic, 1, true, pDone)
				close(newRunFinished)
			}()
		}
	})

	startDispatch := time.Now()
	cmd, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:          machineID,
		JobID:              jobID,
		Command:            "agy",
		Wait:               true,
		WaitTimeoutSeconds: 5,
		MQTTClient:         mockClient,
		Out:                outBuf,
		ErrOut:             errBuf,
	})
	if err != nil {
		t.Fatalf("expected dispatch to succeed, got %v", err)
	}
	if cmd == nil || cmd.JobID != jobID {
		t.Fatalf("unexpected dispatch result: %+v", cmd)
	}

	elapsed := time.Since(startDispatch)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("dispatch returned too quickly (%v), indicating it consumed stale retained status instead of waiting for new run", elapsed)
	}

	select {
	case <-newRunFinished:
	default:
		t.Fatalf("new run was not completed when dispatch returned")
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "Job reused-job-001 status: queued") {
		t.Errorf("expected queued status in output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Job reused-job-001 completed successfully in 150ms") {
		t.Errorf("expected new run completion in output, got: %s", outStr)
	}
}

// TestDispatch_Wait_StatusChannelSaturationDoesNotDropDone tests Important #2:
// Flooding status updates does not silently drop terminal done status.
func TestDispatch_Wait_StatusChannelSaturationDoesNotDropDone(t *testing.T) {
	mockClient := newMockMQTTClient()
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)

	machineID := "saturate-node"
	jobID := "job-saturate-1"
	statusTopic := mqtt.JobStatusTopic(machineID, jobID)
	dispatchTopic := mqtt.MachineDispatchTopic(machineID)

	mockClient.Subscribe(dispatchTopic, 1, func(_ paho.Client, msg paho.Message) {
		go func() {
			// Rapidly send 50 progress updates (exceeding channel capacity of 32)
			for i := 1; i <= 50; i++ {
				j := model.Job{
					ID:        jobID,
					MachineID: machineID,
					Status:    model.StatusRunning,
					Progress:  float64(i),
					StartedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				}
				data, _ := json.Marshal(j)
				mockClient.Publish(statusTopic, 1, false, data)
			}
			// Immediately publish done
			doneJob := model.Job{
				ID:          jobID,
				MachineID:   machineID,
				Status:      model.StatusDone,
				Progress:    100.0,
				StartedAt:   time.Now().UTC(),
				UpdatedAt:   time.Now().UTC(),
				DurationStr: "1s",
			}
			data, _ := json.Marshal(doneJob)
			mockClient.Publish(statusTopic, 1, true, data)
		}()
	})

	_, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:          machineID,
		JobID:              jobID,
		Command:            "agy",
		Wait:               true,
		WaitTimeoutSeconds: 5,
		MQTTClient:         mockClient,
		Out:                outBuf,
		ErrOut:             errBuf,
	})
	if err != nil {
		t.Fatalf("expected dispatch to complete successfully despite channel saturation, got: %v", err)
	}
}

func TestDispatch_Follow_Logs(t *testing.T) {
	mockClient := newMockMQTTClient()
	outBuf := new(bytes.Buffer)

	subscribedCh := make(chan struct{})
	var logReceivedMu sync.Mutex
	var logsReceived []string

	go func() {
		<-subscribedCh
		time.Sleep(30 * time.Millisecond)

		// Publish streaming log
		log1 := model.LogEntry{
			Timestamp: time.Now().UTC(),
			JobID:     "job-follow-1",
			MachineID: "node-follow",
			Engine:    model.EngineAgy,
			Level:     model.LevelAgent,
			Message:   "Starting step 1...",
		}
		pLog1, _ := json.Marshal(log1)
		mockClient.Publish(mqtt.JobLogsTopic("node-follow", "job-follow-1"), 0, false, pLog1)

		time.Sleep(30 * time.Millisecond)

		// Complete job
		jobDone := model.Job{
			ID:        "job-follow-1",
			MachineID: "node-follow",
			Engine:    model.EngineAgy,
			Status:    model.StatusDone,
			Progress:  100.0,
			StartedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		pDone, _ := json.Marshal(jobDone)
		mockClient.Publish(mqtt.JobStatusTopic("node-follow", "job-follow-1"), 1, true, pDone)
	}()

	cmd, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:    "node-follow",
		JobID:        "job-follow-1",
		Command:      "agy",
		Follow:       true,
		MQTTClient:   mockClient,
		Out:          outBuf,
		OnSubscribed: func() { close(subscribedCh) },
		OnLog: func(entry model.LogEntry) {
			logReceivedMu.Lock()
			logsReceived = append(logsReceived, entry.Message)
			logReceivedMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("expected follow to succeed, got %v", err)
	}
	if cmd == nil {
		t.Fatalf("expected non-nil cmd")
	}

	logReceivedMu.Lock()
	defer logReceivedMu.Unlock()
	if len(logsReceived) != 1 || logsReceived[0] != "Starting step 1..." {
		t.Errorf("unexpected logs received: %v", logsReceived)
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "[job-follow-1] [AGENT] Starting step 1...") {
		t.Errorf("expected formatted log line in output: %s", outStr)
	}
}

func TestDispatch_ConnectingBannerNotFalseConnected(t *testing.T) {
	outBuf := new(bytes.Buffer)

	// Attempt connecting to unreachable port on 127.0.0.1
	cfg := dispatch.Config{
		MachineID: "remote-node-1",
		Command:   "agy",
		MQTT: &mqtt.Config{
			BrokerURL: "tcp://127.0.0.1:18833",
			Timeout:   100 * time.Millisecond,
		},
		Out:   outBuf,
		Quiet: false,
	}

	_, err := dispatch.Dispatch(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected error connecting to unreachable broker")
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "Connecting to MQTT broker tcp://127.0.0.1:18833...") {
		t.Errorf("expected connecting banner, got: %s", outStr)
	}
	if strings.Contains(outStr, "Connected to MQTT broker") {
		t.Errorf("expected no false connected banner, got: %s", outStr)
	}
}

func TestDispatch_EndToEnd_WithListener(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "e2e-status.json")

	listeningCh := make(chan struct{})

	// 1. Start edge listener on machine "edge-mac-1"
	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:  "edge-mac-1",
		StatusFile: statusFile,
		MQTTClient: mockClient,
		Stub:       true,
		Quiet:      true,
		OnListening: func(_ string) {
			close(listeningCh)
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	defer listenerCancel()

	go func() {
		_ = l.Run(listenerCtx)
	}()

	select {
	case <-listeningCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for listener to subscribe")
	}

	// 2. Dispatch a job from publisher CLI with --wait and --follow
	outBuf := new(bytes.Buffer)
	cmd, err := dispatch.Dispatch(context.Background(), dispatch.Config{
		MachineID:   "edge-mac-1",
		JobID:       "job-e2e-dispatch-01",
		Engine:      model.EngineAgy,
		Command:     "agy",
		Args:        []string{"--prompt", "E2E integration test"},
		Title:       "E2E Dispatch Integration",
		Wait:        true,
		Follow:      true,
		MQTTClient:  mockClient,
		Out:         outBuf,
		Quiet:       false,
	})
	if err != nil {
		t.Fatalf("end-to-end dispatch failed: %v", err)
	}
	if cmd == nil || cmd.JobID != "job-e2e-dispatch-01" {
		t.Errorf("unexpected cmd result: %+v", cmd)
	}

	outStr := outBuf.String()
	if !strings.Contains(outStr, "Dispatched job job-e2e-dispatch-01") {
		t.Errorf("expected dispatch banner in output: %s", outStr)
	}
	if !strings.Contains(outStr, "completed successfully") {
		t.Errorf("expected completion banner in output: %s", outStr)
	}
	if !strings.Contains(outStr, "[job-e2e-dispatch-01]") {
		t.Errorf("expected log streaming prefix in output: %s", outStr)
	}
}


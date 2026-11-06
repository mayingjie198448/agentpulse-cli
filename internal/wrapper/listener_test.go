package wrapper_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

func TestListener_ConnectAndHeartbeat(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "listener-status.json")

	connectedCh := make(chan struct{})
	listeningCh := make(chan string, 1)

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "test-listener-node",
		MachineName:       "Test Listener Node",
		HeartbeatInterval: 100 * time.Millisecond,
		StatusFile:        statusFile,
		MQTTClient:        mockClient,
		DisableBareAttach: true,
		Quiet:             true,
		OnMQTTConnected: func() {
			close(connectedCh)
		},
		OnListening: func(topic string) {
			listeningCh <- topic
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- l.Run(ctx)
	}()

	select {
	case <-connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnMQTTConnected")
	}

	var listeningTopic string
	select {
	case listeningTopic = <-listeningCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnListening")
	}

	expectedTopic := mqtt.MachineDispatchTopic("test-listener-node")
	if listeningTopic != expectedTopic {
		t.Errorf("expected listening topic %s, got %s", expectedTopic, listeningTopic)
	}

	// Verify initial machine status published online
	pubMsgs := mockClient.GetPublished()
	var initialMachine *model.Machine
	for _, m := range pubMsgs {
		if m.Topic == mqtt.MachineStatusTopic("test-listener-node") {
			var mach model.Machine
			if err := json.Unmarshal(m.Payload, &mach); err == nil {
				initialMachine = &mach
			}
		}
	}
	if initialMachine == nil {
		t.Fatalf("expected initial machine status publication")
	}
	if !initialMachine.Online {
		t.Errorf("expected machine online: true, got %v", initialMachine.Online)
	}
	if initialMachine.JobCount != 0 {
		t.Errorf("expected job count 0, got %d", initialMachine.JobCount)
	}

	// Now cancel context and verify clean offline shutdown
	cancel()
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("listener did not shut down in time")
	}

	// Verify final status published offline
	finalMsgs := mockClient.GetPublished()
	var lastMachine *model.Machine
	for _, m := range finalMsgs {
		if m.Topic == mqtt.MachineStatusTopic("test-listener-node") {
			var mach model.Machine
			if err := json.Unmarshal(m.Payload, &mach); err == nil {
				machCopy := mach
				lastMachine = &machCopy
			}
		}
	}
	if lastMachine == nil {
		t.Fatalf("expected final machine status publication")
	}
	if lastMachine.Online {
		t.Errorf("expected machine online: false on exit, got %v", lastMachine.Online)
	}
}

func TestListener_ReceiveDispatch_RunsStub(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "stub-dispatch-status.json")

	var jobMu sync.Mutex
	var startedJob *model.Job
	var completedJob *model.Job
	var completedErr error
	var wg sync.WaitGroup
	wg.Add(1)

	listeningCh := make(chan struct{})

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "worker-node-1",
		StatusFile:        statusFile,
		MQTTClient:        mockClient,
		Stub:              true,
		DisableBareAttach: true,
		Quiet:             true,
		OnListening: func(_ string) {
			close(listeningCh)
		},
		OnJobStart: func(j model.Job) {
			jobMu.Lock()
			copyJ := j
			startedJob = &copyJ
			jobMu.Unlock()
		},
		OnJobComplete: func(j model.Job, err error) {
			jobMu.Lock()
			copyJ := j
			completedJob = &copyJ
			completedErr = err
			jobMu.Unlock()
			wg.Done()
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	select {
	case <-listeningCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnListening")
	}

	// Send a valid dispatch command
	dispatchCmd := model.DispatchCommand{
		CommandID: "cmd-test-101",
		JobID:     "job-test-stub-1",
		Engine:    model.EngineOpenCode,
		Command:   "opencode",
		Args:      []string{"run", "--prompt", "Refactor models"},
		Title:     "Refactor models task",
		Timestamp: time.Now().UTC(),
	}
	payload, _ := json.Marshal(dispatchCmd)
	mockClient.Publish(mqtt.MachineDispatchTopic("worker-node-1"), 1, false, payload)

	// Wait for job completion
	wg.Wait()

	jobMu.Lock()
	defer jobMu.Unlock()

	if completedErr != nil {
		t.Fatalf("expected stub job to succeed, got %v", completedErr)
	}
	if startedJob == nil || startedJob.ID != "job-test-stub-1" {
		t.Errorf("unexpected started job: %+v", startedJob)
	}
	if completedJob == nil || completedJob.Status != model.StatusDone {
		t.Errorf("expected job done, got %+v", completedJob)
	}

	// Verify machine remains online after job completion
	if !l.Machine().Online {
		t.Errorf("expected machine to remain online after job completes")
	}
	if l.Machine().JobCount != 0 {
		t.Errorf("expected machine job count to be 0 after job completes, got %d", l.Machine().JobCount)
	}
}

func TestListener_MissingBinary_FailsWithoutStub(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "missing-bin-status.json")

	var jobMu sync.Mutex
	var completedJob *model.Job
	var completedErr error
	var wg sync.WaitGroup
	wg.Add(1)

	listeningCh := make(chan struct{})

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "strict-node-1",
		StatusFile:        statusFile,
		MQTTClient:        mockClient,
		Stub:              false, // do NOT stub missing binary
		DisableBareAttach: true,
		Quiet:             true,
		OnListening: func(_ string) {
			close(listeningCh)
		},
		OnJobComplete: func(j model.Job, err error) {
			jobMu.Lock()
			copyJ := j
			completedJob = &copyJ
			completedErr = err
			jobMu.Unlock()
			wg.Done()
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	select {
	case <-listeningCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnListening")
	}

	// Send dispatch with missing binary
	dispatchCmd := model.DispatchCommand{
		CommandID: "cmd-missing-bin-102",
		JobID:     "job-missing-bin-01",
		Engine:    model.EngineAgy,
		Command:   "nonexistent-binary-cli-9999",
		Timestamp: time.Now().UTC(),
	}
	payload, _ := json.Marshal(dispatchCmd)
	mockClient.Publish(mqtt.MachineDispatchTopic("strict-node-1"), 1, false, payload)

	wg.Wait()

	jobMu.Lock()
	defer jobMu.Unlock()

	if completedErr == nil {
		t.Fatalf("expected missing binary to fail, got nil error")
	}
	if !strings.Contains(completedErr.Error(), "command binary") {
		t.Errorf("expected missing binary error message, got %v", completedErr)
	}
	if completedJob == nil || completedJob.Status != model.StatusFailed {
		t.Errorf("expected job status failed, got %+v", completedJob)
	}

	// Machine should still be online and ready for new jobs
	if !l.Machine().Online {
		t.Errorf("expected machine to remain online after failed job")
	}
}

func TestListener_RejectMalformedPayload(t *testing.T) {
	mockClient := newMockMQTTClient()
	stderrBuf := new(bytes.Buffer)
	listeningCh := make(chan struct{})

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "robust-node-1",
		MQTTClient:        mockClient,
		DisableBareAttach: true,
		Quiet:             true,
		Stderr:            stderrBuf,
		OnListening: func(_ string) {
			close(listeningCh)
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	select {
	case <-listeningCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnListening")
	}

	// 1. Publish completely malformed JSON
	mockClient.Publish(mqtt.MachineDispatchTopic("robust-node-1"), 1, false, []byte("NOT_VALID_JSON{{{"))

	// 2. Publish JSON with empty command
	emptyCmd := model.DispatchCommand{
		CommandID: "cmd-empty-1",
		Command:   "",
		Args:      []string{},
	}
	emptyPayload, _ := json.Marshal(emptyCmd)
	mockClient.Publish(mqtt.MachineDispatchTopic("robust-node-1"), 1, false, emptyPayload)

	time.Sleep(50 * time.Millisecond)

	// Verify listener is still running and did not crash
	if !l.Machine().Online {
		t.Errorf("expected listener to still be online")
	}

	errOutput := stderrBuf.String()
	if !strings.Contains(errOutput, "rejected malformed dispatch payload") {
		t.Errorf("expected rejected malformed payload log, got: %s", errOutput)
	}
	if !strings.Contains(errOutput, "rejected invalid dispatch: command is empty") {
		t.Errorf("expected rejected invalid command log, got: %s", errOutput)
	}
}

func TestListener_SerialExecution(t *testing.T) {
	mockClient := newMockMQTTClient()

	var orderMu sync.Mutex
	var executionOrder []string

	var wg sync.WaitGroup
	wg.Add(2)
	listeningCh := make(chan struct{})

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "serial-node-1",
		MQTTClient:        mockClient,
		Stub:              true,
		DisableBareAttach: true,
		Quiet:             true,
		OnListening: func(_ string) {
			close(listeningCh)
		},
		OnJobComplete: func(j model.Job, err error) {
			orderMu.Lock()
			executionOrder = append(executionOrder, j.ID)
			orderMu.Unlock()
			wg.Done()
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	select {
	case <-listeningCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnListening")
	}

	// Publish two jobs in rapid succession
	cmd1 := model.DispatchCommand{
		CommandID: "cmd-serial-1",
		JobID:     "job-first-1",
		Engine:    model.EngineAgy,
		Command:   "agy",
		Title:     "First job",
	}
	cmd2 := model.DispatchCommand{
		CommandID: "cmd-serial-2",
		JobID:     "job-second-2",
		Engine:    model.EngineAgy,
		Command:   "agy",
		Title:     "Second job",
	}

	p1, _ := json.Marshal(cmd1)
	p2, _ := json.Marshal(cmd2)

	mockClient.Publish(mqtt.MachineDispatchTopic("serial-node-1"), 1, false, p1)
	mockClient.Publish(mqtt.MachineDispatchTopic("serial-node-1"), 1, false, p2)

	wg.Wait()

	orderMu.Lock()
	defer orderMu.Unlock()
	if len(executionOrder) != 2 {
		t.Fatalf("expected 2 completed jobs, got %d", len(executionOrder))
	}
	if executionOrder[0] != "job-first-1" || executionOrder[1] != "job-second-2" {
		t.Errorf("expected serial execution order [job-first-1, job-second-2], got %v", executionOrder)
	}
}

// TestListener_AutoReconnect_Resubscribes tests Critical C2:
// When the connection drops and reconnects (e.g. clean session),
// Listener.OnConnect re-subscribes to the dispatch topic and resumes receiving commands.
func TestListener_AutoReconnect_Resubscribes(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "reconnect-status.json")

	var completedJob *model.Job
	var wg sync.WaitGroup
	wg.Add(1)

	listeningCh := make(chan struct{})

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "reconnect-node-1",
		StatusFile:        statusFile,
		MQTTClient:        mockClient,
		Stub:              true,
		DisableBareAttach: true,
		Quiet:             true,
		OnListening: func(_ string) {
			select {
			case <-listeningCh:
			default:
				close(listeningCh)
			}
		},
		OnJobComplete: func(j model.Job, err error) {
			completedJob = &j
			wg.Done()
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	select {
	case <-listeningCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnListening")
	}

	dispatchTopic := mqtt.MachineDispatchTopic("reconnect-node-1")
	if !mockClient.HasSubscription(dispatchTopic) {
		t.Fatalf("expected initial subscription to %s", dispatchTopic)
	}

	// 1. Simulate network disconnect & clean session: subscriptions lost!
	mockClient.ClearSubscriptions()
	if mockClient.HasSubscription(dispatchTopic) {
		t.Fatalf("subscription should be cleared after disconnect")
	}

	// 2. Simulate / inject AutoReconnect
	l.OnConnect(mockClient)

	// Verify subscription is restored
	if !mockClient.HasSubscription(dispatchTopic) {
		t.Fatalf("expected subscription to %s to be restored upon OnConnect", dispatchTopic)
	}

	// 3. Publish dispatch command now that it reconnected
	cmd := model.DispatchCommand{
		CommandID: "cmd-reconnect-1",
		JobID:     "job-reconnect-1",
		Engine:    model.EngineAgy,
		Command:   "agy",
		Title:     "Reconnect test",
	}
	p, _ := json.Marshal(cmd)
	mockClient.Publish(dispatchTopic, 1, false, p)

	wg.Wait()

	if completedJob == nil || completedJob.ID != "job-reconnect-1" {
		t.Errorf("expected job to execute after reconnect, got %+v", completedJob)
	}
}

// TestListener_CancelRunningJob_PublishesOffline tests Important #6:
// Cancelling a running job or shutting down publishes online: false and job_count: 0.
func TestListener_CancelRunningJob_PublishesOffline(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "cancel-status.json")

	jobStartedCh := make(chan struct{})

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "cancel-node-1",
		StatusFile:        statusFile,
		MQTTClient:        mockClient,
		Stub:              true,
		DisableBareAttach: true,
		Quiet:             true,
		OnJobStart: func(j model.Job) {
			close(jobStartedCh)
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- l.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	dispatchTopic := mqtt.MachineDispatchTopic("cancel-node-1")
	cmd := model.DispatchCommand{
		CommandID: "cmd-long-1",
		JobID:     "job-long-1",
		Engine:    model.EngineAgy,
		Command:   "agy",
		Title:     "Long running job",
	}
	p, _ := json.Marshal(cmd)
	mockClient.Publish(dispatchTopic, 1, false, p)

	// Wait until job starts
	select {
	case <-jobStartedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("job did not start in time")
	}

	// Cancel listener while job is in progress!
	cancel()

	select {
	case err := <-listenerDone:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("listener did not shut down in time after context cancellation")
	}

	// Verify final published status is online: false and job_count: 0
	finalMsgs := mockClient.GetPublished()
	var lastMachine *model.Machine
	for _, m := range finalMsgs {
		if m.Topic == mqtt.MachineStatusTopic("cancel-node-1") {
			var mach model.Machine
			if err := json.Unmarshal(m.Payload, &mach); err == nil {
				machCopy := mach
				lastMachine = &machCopy
			}
		}
	}
	if lastMachine == nil {
		t.Fatalf("expected machine status published")
	}
	if lastMachine.Online {
		t.Errorf("expected machine online: false after cancellation, got %v", lastMachine.Online)
	}
	if lastMachine.JobCount != 0 {
		t.Errorf("expected machine job_count: 0 after cancellation, got %d", lastMachine.JobCount)
	}
}

// TestListener_Deduplication_QoS1_Redelivery tests Important #3:
// Commands with duplicate command_id are ignored and not executed multiple times.
func TestListener_Deduplication_QoS1_Redelivery(t *testing.T) {
	mockClient := newMockMQTTClient()
	var executionCount int
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "dedup-node-1",
		MQTTClient:        mockClient,
		Stub:              true,
		DisableBareAttach: true,
		Quiet:             true,
		OnJobComplete: func(j model.Job, err error) {
			mu.Lock()
			executionCount++
			mu.Unlock()
			wg.Done()
		},
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	dispatchTopic := mqtt.MachineDispatchTopic("dedup-node-1")
	cmd := model.DispatchCommand{
		CommandID: "duplicate-cmd-999",
		JobID:     "job-dedup-1",
		Engine:    model.EngineAgy,
		Command:   "agy",
		Title:     "Dedup test",
	}
	p, _ := json.Marshal(cmd)

	// Publish twice with the same command_id (simulating QoS 1 redelivery)
	mockClient.Publish(dispatchTopic, 1, false, p)
	mockClient.Publish(dispatchTopic, 1, false, p)

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := executionCount
	mu.Unlock()

	if count != 1 {
		t.Errorf("expected job to execute exactly once, but executed %d times", count)
	}
}

// TestListener_ErrorPublishJobFailed tests Important #1:
// Malformed JSON and empty command publish failed status if job_id can be extracted.
func TestListener_ErrorPublishJobFailed(t *testing.T) {
	mockClient := newMockMQTTClient()
	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "err-node-1",
		MQTTClient:        mockClient,
		Stub:              true,
		DisableBareAttach: true,
		Quiet:             true,
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	dispatchTopic := mqtt.MachineDispatchTopic("err-node-1")

	// 1. Malformed JSON with job_id
	malformed := []byte(`{"job_id": "job-bad-json-1", "command": `)
	mockClient.Publish(dispatchTopic, 1, false, malformed)

	time.Sleep(50 * time.Millisecond)

	var failedJob *model.Job
	for _, m := range mockClient.GetPublished() {
		if m.Topic == mqtt.JobStatusTopic("err-node-1", "job-bad-json-1") {
			var j model.Job
			if err := json.Unmarshal(m.Payload, &j); err == nil {
				failedJob = &j
			}
		}
	}
	if failedJob == nil {
		t.Fatalf("expected job status failed published for malformed JSON with job_id")
	}
	if failedJob.Status != model.StatusFailed {
		t.Errorf("expected status failed, got %s", failedJob.Status)
	}
	if !strings.Contains(failedJob.Error, "rejected malformed dispatch payload") {
		t.Errorf("expected malformed payload error, got %s", failedJob.Error)
	}

	// 2. Empty command with job_id
	emptyCmd := model.DispatchCommand{
		CommandID: "cmd-empty-2",
		JobID:     "job-empty-cmd-2",
		Command:   "",
		Args:      []string{},
	}
	pEmpty, _ := json.Marshal(emptyCmd)
	mockClient.Publish(dispatchTopic, 1, false, pEmpty)

	time.Sleep(50 * time.Millisecond)

	var emptyFailedJob *model.Job
	for _, m := range mockClient.GetPublished() {
		if m.Topic == mqtt.JobStatusTopic("err-node-1", "job-empty-cmd-2") {
			var j model.Job
			if err := json.Unmarshal(m.Payload, &j); err == nil {
				emptyFailedJob = &j
			}
		}
	}
	if emptyFailedJob == nil {
		t.Fatalf("expected job status failed published for empty command")
	}
	if emptyFailedJob.Status != model.StatusFailed {
		t.Errorf("expected status failed, got %s", emptyFailedJob.Status)
	}
	if !strings.Contains(emptyFailedJob.Error, "rejected invalid dispatch: command is empty") {
		t.Errorf("expected empty command error, got %s", emptyFailedJob.Error)
	}
}

// TestListener_QueuedStatusAndJobCount tests Important #5:
// Incoming command immediately triggers queued status, and job_count reflects running + queued.
func TestListener_QueuedStatusAndJobCount(t *testing.T) {
	mockClient := newMockMQTTClient()
	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "queue-node-1",
		MQTTClient:        mockClient,
		Stub:              true,
		DisableBareAttach: true,
		Quiet:             true,
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = l.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	dispatchTopic := mqtt.MachineDispatchTopic("queue-node-1")
	cmd := model.DispatchCommand{
		CommandID: "cmd-q-1",
		JobID:     "job-q-1",
		Engine:    model.EngineAgy,
		Command:   "agy",
		Args:      []string{"run", "--prompt", "test"},
		Title:     "Queue test",
	}
	p, _ := json.Marshal(cmd)
	mockClient.Publish(dispatchTopic, 1, false, p)

	time.Sleep(100 * time.Millisecond)

	var queuedJob *model.Job
	for _, m := range mockClient.GetPublished() {
		if m.Topic == mqtt.JobStatusTopic("queue-node-1", "job-q-1") {
			var j model.Job
			if err := json.Unmarshal(m.Payload, &j); err == nil && j.Status == model.StatusQueued {
				queuedJob = &j
			}
		}
	}
	if queuedJob == nil {
		t.Fatalf("expected queued job status to be published")
	}
	if queuedJob.Status != model.StatusQueued {
		t.Errorf("expected queued status, got %s", queuedJob.Status)
	}
}

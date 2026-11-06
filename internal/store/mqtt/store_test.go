package mqtt_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	mqttstore "github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

func TestMQTTStore_RetainedMachines(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Initial check: empty
	machines, err := st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(machines) != 0 {
		t.Fatalf("expected 0 machines, got %d", len(machines))
	}

	// 1. Deliver retained message for mac-studio-m2 (online)
	macPayload := `{"id":"mac-studio-m2","name":"Mac Studio M2 Max","online":true,"platform":"darwin/arm64","job_count":2,"ip":"192.0.2.10"}`
	fakeClient.Deliver("agentpulse/v1/machines/mac-studio-m2/status", []byte(macPayload), true)

	machines, err = st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines error: %v", err)
	}
	if len(machines) != 1 {
		t.Fatalf("expected 1 machine, got %d", len(machines))
	}
	if machines[0].ID != "mac-studio-m2" || !machines[0].Online || machines[0].JobCount != 2 {
		t.Errorf("unexpected machine data: %+v", machines[0])
	}

	// GetMachine
	m, err := st.GetMachine(ctx, "mac-studio-m2")
	if err != nil {
		t.Fatalf("GetMachine error: %v", err)
	}
	if m.Name != "Mac Studio M2 Max" {
		t.Errorf("expected name 'Mac Studio M2 Max', got %q", m.Name)
	}

	// Non-existent machine
	_, err = st.GetMachine(ctx, "unknown-node")
	if err == nil {
		t.Fatalf("expected error for unknown machine, got nil")
	}

	// 2. Deliver offline machine ccdq
	ccdqPayload := `{"id":"ccdq","name":"ccdq-server","online":false,"platform":"linux/amd64","job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/status", []byte(ccdqPayload), true)

	machines, err = st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines error: %v", err)
	}
	if len(machines) != 2 {
		t.Fatalf("expected 2 machines, got %d", len(machines))
	}
	// Verify sort order: online nodes first
	if machines[0].ID != "mac-studio-m2" || machines[1].ID != "ccdq" {
		t.Errorf("expected online machine first, got order: %s, %s", machines[0].ID, machines[1].ID)
	}
}

func TestMQTTStore_RetainedJobs(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Deliver 2 jobs: one done, one running
	doneJob := `{"id":"job-done-01","machine_id":"ccdq","engine":"opencode","status":"done","progress":100.0,"title":"OpenCode Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/jobs/job-done-01/status", []byte(doneJob), true)

	runningJob := `{"id":"job-run-02","machine_id":"mac-studio-m2","engine":"agy","status":"running","progress":45.0,"title":"Agy Refactor"}`
	fakeClient.Deliver("agentpulse/v1/machines/mac-studio-m2/jobs/job-run-02/status", []byte(runningJob), true)

	jobs, err := st.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	// Verify sort order: running job appears before done job
	if jobs[0].ID != "job-run-02" || jobs[1].ID != "job-done-01" {
		t.Errorf("expected running job first, got: %s, %s", jobs[0].ID, jobs[1].ID)
	}

	// GetJob
	j, err := st.GetJob(ctx, "job-run-02")
	if err != nil {
		t.Fatalf("GetJob error: %v", err)
	}
	if j.Engine != model.EngineAgy || j.Progress != 45.0 {
		t.Errorf("unexpected job: %+v", j)
	}

	// Non-existent job
	_, err = st.GetJob(ctx, "job-non-existent")
	if err == nil {
		t.Fatalf("expected error for non-existent job, got nil")
	}
}

func TestMQTTStore_LogsAndRingBuffer(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.LogCap = 10 // small cap for testing ring truncation

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Initial GetLogs on non-existent job -> error
	_, err = st.GetLogs(ctx, "job-logs-01", 10)
	if err == nil {
		t.Fatalf("expected error for job with no logs or record, got nil")
	}

	// Create job
	jobPayload := `{"id":"job-logs-01","machine_id":"mac-studio-m2","engine":"agy","status":"running"}`
	fakeClient.Deliver("agentpulse/v1/machines/mac-studio-m2/jobs/job-logs-01/status", []byte(jobPayload), true)

	// Now job exists, 0 logs -> empty slice
	emptyLogs, err := st.GetLogs(ctx, "job-logs-01", 10)
	if err != nil {
		t.Fatalf("unexpected error for job with 0 logs: %v", err)
	}
	if len(emptyLogs) != 0 {
		t.Fatalf("expected 0 logs, got %d", len(emptyLogs))
	}

	// Inject 25 logs
	for i := 1; i <= 25; i++ {
		logPayload := fmt.Sprintf(`{"timestamp":"2026-11-02T13:30:%02dZ","job_id":"job-logs-01","machine_id":"mac-studio-m2","engine":"agy","level":"AGENT","message":"step-%d"}`, i, i)
		fakeClient.Deliver("agentpulse/v1/machines/mac-studio-m2/jobs/job-logs-01/logs", []byte(logPayload), false)
	}

	// Verify ring buffer capped at 10
	logs, err := st.GetLogs(ctx, "job-logs-01", 0)
	if err != nil {
		t.Fatalf("GetLogs error: %v", err)
	}
	if len(logs) != 10 {
		t.Fatalf("expected exactly 10 logs (ring cap), got %d", len(logs))
	}
	// Oldest kept should be step-16, newest should be step-25
	if logs[0].Message != "step-16" {
		t.Errorf("expected first log to be step-16, got %q", logs[0].Message)
	}
	if logs[9].Message != "step-25" {
		t.Errorf("expected last log to be step-25, got %q", logs[9].Message)
	}

	// Test limit
	limitLogs, err := st.GetLogs(ctx, "job-logs-01", 3)
	if err != nil {
		t.Fatalf("GetLogs with limit error: %v", err)
	}
	if len(limitLogs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(limitLogs))
	}
	if limitLogs[0].Message != "step-23" || limitLogs[2].Message != "step-25" {
		t.Errorf("unexpected logs with limit 3: %+v", limitLogs)
	}

	// Raw string log fallback
	fakeClient.Deliver("agentpulse/v1/machines/mac-studio-m2/jobs/job-logs-01/logs", []byte("plain text raw log message"), false)
	rawLogs, err := st.GetLogs(ctx, "job-logs-01", 1)
	if err != nil {
		t.Fatalf("GetLogs error: %v", err)
	}
	if len(rawLogs) != 1 || rawLogs[0].Message != "plain text raw log message" {
		t.Errorf("unexpected raw fallback log: %+v", rawLogs)
	}
}

func TestMQTTStore_SubscribeAndEvents(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// 1. Deliver Machine Status -> Expect EventMachineUpdated
	macPayload := `{"id":"ccdq","online":true}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/status", []byte(macPayload), false)

	select {
	case ev := <-eventCh:
		if ev.Type != model.EventMachineUpdated || ev.Machine == nil || ev.Machine.ID != "ccdq" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for EventMachineUpdated")
	}

	// 2. Deliver Job Status -> Expect EventJobUpdated
	jobPayload := `{"id":"job-test-99","status":"running","progress":50.0}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/jobs/job-test-99/status", []byte(jobPayload), false)

	select {
	case ev := <-eventCh:
		if ev.Type != model.EventJobUpdated || ev.Job == nil || ev.Job.ID != "job-test-99" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for EventJobUpdated")
	}

	// 3. Deliver Job Log -> Expect EventLogAdded
	logPayload := `{"message":"compilation started","level":"INFO"}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/jobs/job-test-99/logs", []byte(logPayload), false)

	select {
	case ev := <-eventCh:
		if ev.Type != model.EventLogAdded || ev.Log == nil || ev.Log.Message != "compilation started" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for EventLogAdded")
	}

	// 4. Cancel Context -> Expect channel closed
	cancel()
	select {
	case _, ok := <-eventCh:
		if ok {
			// Might drain previous event, but eventually closes
			for ok {
				_, ok = <-eventCh
			}
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("expected subscriber channel to close on context cancellation")
	}
}

func TestMQTTStore_Backpressure(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.BufferCap = 2 // Small buffer to test backpressure

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	eventCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// Fill buffer with 2 logs
	fakeClient.Deliver("agentpulse/v1/machines/m1/jobs/j1/logs", []byte(`{"message":"log 1"}`), false)
	fakeClient.Deliver("agentpulse/v1/machines/m1/jobs/j1/logs", []byte(`{"message":"log 2"}`), false)

	// Buffer is now full (cap 2). Send 3rd log (should be silently dropped without blocking)
	fakeClient.Deliver("agentpulse/v1/machines/m1/jobs/j1/logs", []byte(`{"message":"log 3"}`), false)

	// Now deliver a critical machine update (should evict an older event to make room)
	fakeClient.Deliver("agentpulse/v1/machines/m1/status", []byte(`{"id":"m1","online":true}`), false)

	// Read events from channel
	var received []model.Event
	drainDone := false
	for !drainDone {
		select {
		case ev := <-eventCh:
			received = append(received, ev)
		default:
			drainDone = true
		}
	}

	if len(received) == 0 {
		t.Fatalf("expected to receive events, got 0")
	}

	// Ensure critical machine update is received in the bounded channel
	hasMachineUpdate := false
	for _, ev := range received {
		if ev.Type == model.EventMachineUpdated && ev.Machine != nil && ev.Machine.ID == "m1" {
			hasMachineUpdate = true
			break
		}
	}
	if !hasMachineUpdate {
		t.Errorf("expected critical machine update to be preserved despite full buffer, received: %+v", received)
	}
}

func TestMQTTStore_Close(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	eventCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// Close store
	if err := st.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Client should be disconnected
	if fakeClient.IsConnected() {
		t.Errorf("expected client to be disconnected")
	}

	// Subscriber channel should be closed
	select {
	case _, ok := <-eventCh:
		if ok {
			t.Errorf("expected subscriber channel to be closed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Errorf("timed out waiting for event channel to close on store shutdown")
	}

	// Further call to Subscribe should fail
	_, err = st.Subscribe(ctx)
	if err == nil {
		t.Errorf("expected error subscribing to closed store, got nil")
	}

	// Double close should be idempotent
	if err := st.Close(); err != nil {
		t.Errorf("expected idempotent Close, got error: %v", err)
	}
}

func TestMQTTStore_HeartbeatTimeout(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.HeartbeatTimeout = 40 * time.Millisecond
	cfg.HeartbeatCheckInterval = 20 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Deliver machine with heartbeat 100ms in the past
	oldTime := time.Now().Add(-100 * time.Millisecond).Format(time.RFC3339)
	payload := fmt.Sprintf(`{"id":"stale-node","name":"Stale Node","online":true,"last_heartbeat":"%s"}`, oldTime)
	fakeClient.Deliver("agentpulse/v1/machines/stale-node/status", []byte(payload), true)

	// Wait for heartbeat checker to run
	time.Sleep(60 * time.Millisecond)

	m, err := st.GetMachine(ctx, "stale-node")
	if err != nil {
		t.Fatalf("GetMachine error: %v", err)
	}
	if m.Online {
		t.Errorf("expected machine to be marked offline after heartbeat timeout, but online is true")
	}
}

// TestMQTTStore_NoSharedPointersRaceRegression tests that broadcast events never share
// pointers with internal store maps and that checkStaleMachines / updates do not mutate
// objects concurrently being read by event subscribers under -race.
func TestMQTTStore_NoSharedPointersRaceRegression(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.HeartbeatTimeout = 20 * time.Millisecond
	cfg.SettleTimeout = 0 // Instant start for this test

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// 1. Deliver Machine status
	oldTime := time.Now().Add(-50 * time.Millisecond).Format(time.RFC3339)
	machinePayload := fmt.Sprintf(`{"id":"race-node","name":"Race Node","online":true,"platform":"linux/amd64","job_count":1,"last_heartbeat":"%s"}`, oldTime)
	fakeClient.Deliver("agentpulse/v1/machines/race-node/status", []byte(machinePayload), false)

	var macEv model.Event
	select {
	case macEv = <-eventCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for machine event")
	}

	if macEv.Machine == nil {
		t.Fatal("expected machine event to have non-nil Machine")
	}

	// Assert pointer isolation: Event Machine pointer must NOT equal store map pointer
	mapMachinePtr := st.RawMachinePointer("race-node")
	if mapMachinePtr == nil {
		t.Fatal("expected store to contain machine race-node")
	}
	if macEv.Machine == mapMachinePtr {
		t.Fatalf("data race blocker: Event.Machine (%p) aliases store map-owned pointer (%p)", macEv.Machine, mapMachinePtr)
	}

	// 2. Deliver Job status
	jobPayload := `{"id":"race-job-01","machine_id":"race-node","engine":"agy","status":"running","progress":15.0}`
	fakeClient.Deliver("agentpulse/v1/machines/race-node/jobs/race-job-01/status", []byte(jobPayload), false)

	var jobEv model.Event
	select {
	case jobEv = <-eventCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job event")
	}

	if jobEv.Job == nil {
		t.Fatal("expected job event to have non-nil Job")
	}

	// Assert pointer isolation: Event Job pointer must NOT equal store map pointer
	mapJobPtr := st.RawJobPointer("race-job-01")
	if mapJobPtr == nil {
		t.Fatal("expected store to contain job race-job-01")
	}
	if jobEv.Job == mapJobPtr {
		t.Fatalf("data race blocker: Event.Job (%p) aliases store map-owned pointer (%p)", jobEv.Job, mapJobPtr)
	}

	// 3. Concurrent read vs mutate test (will trigger -race if pointers alias)
	stopReaders := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	// Subscriber reader 1: continuously reads the broadcast Machine
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopReaders:
				return
			default:
				_ = macEv.Machine.Online
				_ = macEv.Machine.Name
				_ = macEv.Machine.JobCount
				_ = macEv.Machine.LastHeartbeat
				_ = macEv.Machine.Platform
			}
		}
	}()

	// Subscriber reader 2: continuously reads the broadcast Job
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopReaders:
				return
			default:
				_ = jobEv.Job.Status
				_ = jobEv.Job.Progress
				_ = jobEv.Job.UpdatedAt
				_ = jobEv.Job.Command
			}
		}
	}()

	// Store writer: concurrently trigger checkStaleMachines on the exact machine delivered,
	// plus incoming status updates to both machine and job.
	for i := 0; i < 300; i++ {
		// Mutate map in checkStaleMachines
		st.CheckStaleMachines()

		// Deliver job update (would race if jobEv.Job aliased map)
		updatedJob := fmt.Sprintf(`{"id":"race-job-01","machine_id":"race-node","engine":"agy","status":"running","progress":%f}`, float64(i))
		fakeClient.Deliver("agentpulse/v1/machines/race-node/jobs/race-job-01/status", []byte(updatedJob), false)
	}

	close(stopReaders)
	wg.Wait()
}

func TestMQTTStore_ColdStartRetainedSettle(t *testing.T) {
	// Verify default settle duration
	cfg := mqttstore.DefaultConfig()
	if cfg.SettleTimeout != 200*time.Millisecond {
		t.Fatalf("expected DefaultConfig SettleTimeout 200ms, got %v", cfg.SettleTimeout)
	}

	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg.SettleTimeout = 50 * time.Millisecond

	start := time.Now()
	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected NewWithClient to settle for at least ~40ms, got %v", elapsed)
	}

	// Verify explicit Settle helper
	settleStart := time.Now()
	st.Settle(30 * time.Millisecond)
	if time.Since(settleStart) < 25*time.Millisecond {
		t.Fatalf("expected st.Settle to wait for duration")
	}
}

func TestMQTTStore_TimeoutConfigDefaultsAndValidation(t *testing.T) {
	// 1. Verify DefaultConfig Timeout is 10s
	cfg := mqttstore.DefaultConfig()
	if cfg.Timeout != 10*time.Second {
		t.Errorf("expected DefaultConfig Timeout 10s, got %v", cfg.Timeout)
	}

	// 2. Validate fills zero values with defaults
	zeroCfg := mqttstore.Config{
		BrokerURL: "tcp://broker.example:1883",
	}
	if err := zeroCfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if zeroCfg.Timeout != 10*time.Second {
		t.Errorf("expected Validate to default Timeout to 10s, got %v", zeroCfg.Timeout)
	}

	// 3. Custom value is preserved
	customCfg := mqttstore.Config{
		BrokerURL: "tcp://broker.example:1883",
		Timeout:   3 * time.Second,
	}
	if err := customCfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if customCfg.Timeout != 3*time.Second {
		t.Errorf("expected custom Timeout 3s, got %v", customCfg.Timeout)
	}
}

func TestMQTTStore_HeartbeatConfigDefaultsAndValidation(t *testing.T) {
	// 1. Verify default values
	cfg := mqttstore.DefaultConfig()
	if cfg.HeartbeatTimeout != 45*time.Second {
		t.Errorf("expected DefaultConfig HeartbeatTimeout 45s, got %v", cfg.HeartbeatTimeout)
	}
	if cfg.HeartbeatCheckInterval != 10*time.Second {
		t.Errorf("expected DefaultConfig HeartbeatCheckInterval 10s, got %v", cfg.HeartbeatCheckInterval)
	}

	// 2. Validate fills zero values with defaults
	zeroCfg := mqttstore.Config{
		BrokerURL: "tcp://broker.example:1883",
	}
	if err := zeroCfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if zeroCfg.HeartbeatTimeout != 45*time.Second {
		t.Errorf("expected Validate to default HeartbeatTimeout to 45s, got %v", zeroCfg.HeartbeatTimeout)
	}
	if zeroCfg.HeartbeatCheckInterval != 10*time.Second {
		t.Errorf("expected Validate to default HeartbeatCheckInterval to 10s, got %v", zeroCfg.HeartbeatCheckInterval)
	}

	// 3. Custom values are preserved
	customCfg := mqttstore.Config{
		BrokerURL:              "tcp://broker.example:1883",
		HeartbeatTimeout:       30 * time.Second,
		HeartbeatCheckInterval: 5 * time.Second,
	}
	if err := customCfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if customCfg.HeartbeatTimeout != 30*time.Second {
		t.Errorf("expected HeartbeatTimeout 30s, got %v", customCfg.HeartbeatTimeout)
	}
	if customCfg.HeartbeatCheckInterval != 5*time.Second {
		t.Errorf("expected HeartbeatCheckInterval 5s, got %v", customCfg.HeartbeatCheckInterval)
	}
}

// TestMQTTStore_ImmediateStaleMachineCheckOnStartup verifies Important #3:
// When connecting to a broker with retained stale machine heartbeats,
// the store immediately sweeps stale machines upon startup/settle so list --online
// does not falsely report them as online during the first check interval.
func TestMQTTStore_ImmediateStaleMachineCheckOnStartup(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.HeartbeatTimeout = 45 * time.Second
	cfg.HeartbeatCheckInterval = 10 * time.Minute // Large interval: ticker won't fire during test
	cfg.SettleTimeout = 10 * time.Millisecond

	// Broker retained status: machine with online: true but last_heartbeat 5 minutes ago (stale)
	staleTime := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
	payload := fmt.Sprintf(`{"id":"stale-node","name":"Stale Node","online":true,"last_heartbeat":"%s"}`, staleTime)
	fakeClient.Deliver("agentpulse/v1/machines/stale-node/status", []byte(payload), true)

	// Online node with fresh heartbeat
	freshTime := time.Now().Add(-2 * time.Second).Format(time.RFC3339)
	payloadFresh := fmt.Sprintf(`{"id":"fresh-node","name":"Fresh Node","online":true,"last_heartbeat":"%s"}`, freshTime)
	fakeClient.Deliver("agentpulse/v1/machines/fresh-node/status", []byte(payloadFresh), true)

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// Immediately query ListMachines without waiting for ticker
	machines, err := st.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("ListMachines failed: %v", err)
	}

	foundStale := false
	foundFresh := false
	for _, m := range machines {
		if m.ID == "stale-node" {
			foundStale = true
			if m.Online {
				t.Errorf("expected stale-node to be marked OFFLINE immediately upon store startup, got online: true")
			}
		}
		if m.ID == "fresh-node" {
			foundFresh = true
			if !m.Online {
				t.Errorf("expected fresh-node to remain ONLINE, got online: false")
			}
		}
	}

	if !foundStale {
		t.Errorf("stale-node not found in ListMachines")
	}
	if !foundFresh {
		t.Errorf("fresh-node not found in ListMachines")
	}

	// Verify GetMachine also reflects offline
	m, err := st.GetMachine(context.Background(), "stale-node")
	if err != nil {
		t.Fatalf("GetMachine failed: %v", err)
	}
	if m.Online {
		t.Errorf("expected GetMachine('stale-node') to be offline, got online: true")
	}
}

func TestMQTTStore_Announce(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Initial announce list should be empty
	announces, err := st.ListAnnounces(ctx)
	if err != nil {
		t.Fatalf("ListAnnounces failed: %v", err)
	}
	if len(announces) != 0 {
		t.Fatalf("expected 0 announces, got %d", len(announces))
	}

	// 1. Deliver announce rev 1
	payload1 := `{"v":1,"machine_id":"mac-mini","ts":"2026-11-03T08:00:00Z","rev":1,"hostname":"mini","os":"darwin/arm64","agents":[{"id":"agy","alias":["agy"],"ok":true,"detail":"/bin/agy"}]}`
	fakeClient.Deliver("agentpulse/v1/mac-mini/announce", []byte(payload1), true)

	ann, err := st.GetAnnounce(ctx, "mac-mini")
	if err != nil {
		t.Fatalf("GetAnnounce failed: %v", err)
	}
	if ann.Rev != 1 || len(ann.Agents) != 1 || ann.Agents[0].ID != "agy" {
		t.Errorf("unexpected announce: %+v", ann)
	}

	// 2. Deliver older rev 0 (conflict resolution: should be rejected)
	payloadOlder := `{"v":1,"machine_id":"mac-mini","ts":"2026-11-03T09:00:00Z","rev":0,"hostname":"mini","os":"darwin/arm64","agents":[]}`
	fakeClient.Deliver("agentpulse/v1/mac-mini/announce", []byte(payloadOlder), true)

	ann, err = st.GetAnnounce(ctx, "mac-mini")
	if err != nil {
		t.Fatalf("GetAnnounce failed: %v", err)
	}
	if ann.Rev != 1 || len(ann.Agents) != 1 {
		t.Errorf("expected rev 1 to remain, got rev %d with %d agents", ann.Rev, len(ann.Agents))
	}

	// 3. Deliver newer rev 2 with updated agents
	payload2 := `{"v":1,"machine_id":"mac-mini","ts":"2026-11-03T08:05:00Z","rev":2,"hostname":"mini","os":"darwin/arm64","agents":[{"id":"agy","alias":["agy"],"ok":true,"detail":"/bin/agy"},{"id":"opencode","alias":["op","opencode"],"ok":true,"detail":"/bin/opencode"}]}`
	fakeClient.Deliver("agentpulse/v1/mac-mini/announce", []byte(payload2), true)

	ann, err = st.GetAnnounce(ctx, "mac-mini")
	if err != nil {
		t.Fatalf("GetAnnounce failed: %v", err)
	}
	if ann.Rev != 2 || len(ann.Agents) != 2 {
		t.Errorf("expected rev 2 with 2 agents, got %+v", ann)
	}

	// 4. ListAnnounces should now contain mac-mini
	announces, err = st.ListAnnounces(ctx)
	if err != nil {
		t.Fatalf("ListAnnounces failed: %v", err)
	}
	if len(announces) != 1 || announces[0].MachineID != "mac-mini" {
		t.Errorf("unexpected ListAnnounces: %+v", announces)
	}
}

func TestMQTTStore_DeleteJob(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	subCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}

	// Deliver 2 jobs: one done, one running
	donePayload := `{"id":"job-done-01","machine_id":"ccdq","engine":"opencode","status":"done","progress":100.0,"title":"Done Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/jobs/job-done-01/status", []byte(donePayload), true)

	runningPayload := `{"id":"job-run-02","machine_id":"ccdq","engine":"agy","status":"running","progress":50.0,"title":"Running Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/jobs/job-run-02/status", []byte(runningPayload), true)

	// Deliver a log for job-done-01
	logPayload := `{"job_id":"job-done-01","machine_id":"ccdq","level":"INFO","message":"Finished execution"}`
	fakeClient.Deliver("agentpulse/v1/machines/ccdq/jobs/job-done-01/logs", []byte(logPayload), false)

	// Verify both exist initially
	jobs, err := st.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	// 1. Refuse deleting running job
	if err := st.DeleteJob(ctx, "job-run-02"); err == nil {
		t.Fatalf("expected error when deleting running job, got nil")
	}

	// 2. Delete non-existent job
	if err := st.DeleteJob(ctx, "unknown-job"); err == nil {
		t.Fatalf("expected error when deleting non-existent job, got nil")
	}

	// 3. Delete done job
	if err := st.DeleteJob(ctx, "job-done-01"); err != nil {
		t.Fatalf("DeleteJob failed for job-done-01: %v", err)
	}

	// 4. Verify in-memory list has only job-run-02
	jobs, err = st.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "job-run-02" {
		t.Fatalf("expected only job-run-02 to remain, got %+v", jobs)
	}

	// 5. Verify logs removed
	if _, err := st.GetLogs(ctx, "job-done-01", 10); err == nil {
		t.Fatalf("expected GetLogs for deleted job to fail, got nil error")
	}

	// 6. Verify MQTT publish with clear-retained (empty payload, Retained=true, QoS=1)
	published := fakeClient.PublishedMessages()
	var clearMsg *mqttstore.PublishedMessage
	for i := range published {
		if published[i].Topic == "agentpulse/v1/machines/ccdq/jobs/job-done-01/status" {
			clearMsg = &published[i]
			break
		}
	}
	if clearMsg == nil {
		t.Fatalf("expected clear-retained publish for job-done-01, found none in %+v", published)
	}
	if !clearMsg.Retained {
		t.Errorf("expected clearMsg.Retained to be true")
	}
	if clearMsg.QoS != 1 {
		t.Errorf("expected clearMsg.QoS to be 1, got %d", clearMsg.QoS)
	}
	if len(clearMsg.Payload) != 0 {
		t.Errorf("expected empty payload for clear-retained tombstone, got %d bytes", len(clearMsg.Payload))
	}

	// 7. Verify EventJobDeleted event was broadcast
	var foundDeletedEvent bool
	timeout := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case ev := <-subCh:
			if ev.Type == model.EventJobDeleted && ev.Job != nil && ev.Job.ID == "job-done-01" {
				foundDeletedEvent = true
				break loop
			}
		case <-timeout:
			break loop
		}
	}
	if !foundDeletedEvent {
		t.Errorf("expected EventJobDeleted for job-done-01 to be broadcast")
	}
}

func TestMQTTStore_TombstoneClearsJob(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initial deliver
	donePayload := `{"id":"job-ext-01","machine_id":"node-ext","status":"done","title":"Ext Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/node-ext/jobs/job-ext-01/status", []byte(donePayload), true)

	job, err := st.GetJob(ctx, "job-ext-01")
	if err != nil || job.ID != "job-ext-01" {
		t.Fatalf("expected job-ext-01 to exist, got %v", err)
	}

	// Broker delivers empty payload tombstone (e.g. from another client)
	fakeClient.Deliver("agentpulse/v1/machines/node-ext/jobs/job-ext-01/status", []byte{}, true)

	// Verify job is removed from store
	if _, err := st.GetJob(ctx, "job-ext-01"); err == nil {
		t.Fatalf("expected job-ext-01 to be removed after tombstone, but still exists")
	}
}

func TestMQTTStore_TombstoneClearsMachine(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	machPayload := `{"id":"mach-del","name":"mach-del","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-del/status", []byte(machPayload), true)

	m, err := st.GetMachine(ctx, "mach-del")
	if err != nil || m.ID != "mach-del" {
		t.Fatalf("expected mach-del to exist, got %v", err)
	}

	// Empty payload tombstone
	fakeClient.Deliver("agentpulse/v1/machines/mach-del/status", []byte{}, true)

	if _, err := st.GetMachine(ctx, "mach-del"); err == nil {
		t.Fatalf("expected mach-del to be removed after tombstone")
	}
}

func TestMQTTStore_TombstoneClearsAnnounce(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	annPayload := `{"v":1,"machine_id":"ann-mach","ts":"2026-11-06T10:00:00Z","rev":1,"hostname":"ann-host","os":"darwin/arm64","agents":[]}`
	fakeClient.Deliver("agentpulse/v1/machines/ann-mach/announce", []byte(annPayload), true)

	ann, err := st.GetAnnounce(ctx, "ann-mach")
	if err != nil || ann.MachineID != "ann-mach" {
		t.Fatalf("expected ann-mach announce to exist, got %v", err)
	}

	// Empty payload tombstone
	fakeClient.Deliver("agentpulse/v1/machines/ann-mach/announce", []byte{}, true)

	if _, err := st.GetAnnounce(ctx, "ann-mach"); err == nil {
		t.Fatalf("expected ann-mach announce to be removed after tombstone")
	}
}

func TestMQTTStore_Refresh(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 10 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initial deliver: node-1 and node-stale
	mach1 := `{"id":"node-1","name":"node-1","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/node-1/status", []byte(mach1), true)

	machStale := `{"id":"node-stale","name":"node-stale","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/node-stale/status", []byte(machStale), true)

	if _, err := st.GetMachine(ctx, "node-stale"); err != nil {
		t.Fatalf("expected node-stale to exist")
	}

	// Stale node retained message cleared on broker (tombstone)
	fakeClient.Deliver("agentpulse/v1/machines/node-stale/status", []byte{}, true)

	// New node announced on broker
	machNew := `{"id":"node-new","name":"node-new","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/node-new/status", []byte(machNew), true)

	// Refresh re-subscribes and settles retained messages
	if err := st.Refresh(ctx); err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	machines, err := st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines error: %v", err)
	}
	if len(machines) != 2 {
		t.Fatalf("expected 2 machines after refresh, got %d: %+v", len(machines), machines)
	}

	machIDs := make(map[string]bool)
	for _, m := range machines {
		machIDs[m.ID] = true
	}
	if !machIDs["node-1"] || !machIDs["node-new"] || machIDs["node-stale"] {
		t.Fatalf("expected node-1 and node-new, node-stale dropped, got: %+v", machIDs)
	}
}

func TestMQTTStore_Refresh_Transactional_ClientNil(t *testing.T) {
	cfg := mqttstore.DefaultConfig()
	st, err := mqttstore.NewWithClient(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Direct message injection before refresh
	mach := `{"id":"mach-persist","name":"mach-persist","online":true,"job_count":0}`
	st.HandleMessage("agentpulse/v1/machines/mach-persist/status", []byte(mach))

	// Refresh must return error when client is nil
	if err := st.Refresh(ctx); err == nil {
		t.Fatalf("expected Refresh error when client is nil, got nil")
	}

	// Existing store state must remain unchanged
	m, err := st.GetMachine(ctx, "mach-persist")
	if err != nil || m == nil || m.ID != "mach-persist" {
		t.Fatalf("expected mach-persist to remain intact after failed refresh, got m=%v, err=%v", m, err)
	}
}

func TestMQTTStore_Refresh_Transactional_Disconnected(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 10 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Initial message
	mach := `{"id":"mach-persist","name":"mach-persist","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-persist/status", []byte(mach), true)

	job := `{"id":"job-persist","machine_id":"mach-persist","status":"running","title":"Job 1"}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-persist/jobs/job-persist/status", []byte(job), true)

	// Simulate client disconnected
	fakeClient.Disconnect(0)

	// Refresh must fail and NOT clear store
	if err := st.Refresh(ctx); err == nil {
		t.Fatalf("expected Refresh error when disconnected, got nil")
	}

	machines, err := st.ListMachines(ctx)
	if err != nil || len(machines) != 1 || machines[0].ID != "mach-persist" {
		t.Fatalf("expected machines to remain unchanged after failed refresh, got %+v", machines)
	}

	jobs, err := st.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "job-persist" {
		t.Fatalf("expected jobs to remain unchanged after failed refresh, got %+v", jobs)
	}
}

func TestMQTTStore_Refresh_Transactional_SubscribeFails(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 10 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mach := `{"id":"mach-safe","name":"mach-safe","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-safe/status", []byte(mach), true)

	// Inject subscription error
	fakeClient.SetSubscribeError(fmt.Errorf("broker subscription rejected"))

	// Refresh must fail
	if err := st.Refresh(ctx); err == nil {
		t.Fatalf("expected Refresh error when subscribe fails, got nil")
	}

	// State must be left untouched
	machines, err := st.ListMachines(ctx)
	if err != nil || len(machines) != 1 || machines[0].ID != "mach-safe" {
		t.Fatalf("expected mach-safe to remain intact after failed subscribe, got %+v", machines)
	}
}

func TestMQTTStore_Refresh_Transactional_SubscribeTimeout_NoPartialUpdate(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.Timeout = 10 * time.Millisecond
	cfg.SettleTimeout = 10 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mach := `{"id":"mach-timeout","name":"mach-timeout","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-timeout/status", []byte(mach), true)

	// Subscribe to events
	subCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// Broker has new retained data that should NOT leak into live store on failure
	fakeClient.Publish("agentpulse/v1/machines/mach-broker-retained/status", 1, true, []byte(`{"id":"mach-broker-retained","online":true}`))

	// Inject subscription timeout
	fakeClient.SetSubscribeTimeout(true)

	// Refresh must fail with timeout
	if err := st.Refresh(ctx); err == nil {
		t.Fatalf("expected Refresh error when subscribe times out, got nil")
	}

	// Live store state must remain untouched
	machines, err := st.ListMachines(ctx)
	if err != nil || len(machines) != 1 || machines[0].ID != "mach-timeout" {
		t.Fatalf("expected original machines to remain intact, got %+v", machines)
	}

	// No events should be delivered to subscriber for aborted retained messages
	select {
	case ev := <-subCh:
		t.Fatalf("expected no events on failed refresh, got: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestMQTTStore_Refresh_ContextCanceled_NoPartialUpdate(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 150 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	mach := `{"id":"mach-cancel","name":"mach-cancel","online":true,"job_count":0}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-cancel/status", []byte(mach), true)

	job := `{"id":"job-cancel","machine_id":"mach-cancel","status":"running","title":"Job Cancel"}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-cancel/jobs/job-cancel/status", []byte(job), true)

	// Broker has different retained message that arrives during subscription
	fakeClient.Publish("agentpulse/v1/machines/mach-ghost/status", 1, true, []byte(`{"id":"mach-ghost","online":true}`))

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel context during settle wait
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	err = st.Refresh(ctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from Refresh, got: %v", err)
	}

	// Store must have original state, mach-ghost must not exist
	checkCtx := context.Background()
	machines, err := st.ListMachines(checkCtx)
	if err != nil || len(machines) != 1 || machines[0].ID != "mach-cancel" {
		t.Fatalf("expected only mach-cancel after canceled refresh, got %+v", machines)
	}

	jobs, err := st.ListJobs(checkCtx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "job-cancel" {
		t.Fatalf("expected only job-cancel after canceled refresh, got %+v", jobs)
	}
}

func TestMQTTStore_Refresh_ConcurrentRealtimeMessages_Success(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 80 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Initial store state
	t0 := time.Now().Add(-10 * time.Minute)
	mach1Init := fmt.Sprintf(`{"id":"mach-1","name":"mach-1","online":true,"last_heartbeat":%q}`, t0.Format(time.RFC3339))
	fakeClient.Deliver("agentpulse/v1/machines/mach-1/status", []byte(mach1Init), true)

	job1Init := `{"id":"job-1","machine_id":"mach-1","status":"running","title":"Job 1","progress":10}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-1/jobs/job-1/status", []byte(job1Init), true)

	subCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// Retained on broker: mach-2 new node
	fakeClient.Publish("agentpulse/v1/machines/mach-2/status", 1, true, []byte(`{"id":"mach-2","name":"mach-2","online":true}`))

	// Run Refresh in background and inject real-time concurrent messages during settle
	refreshErrCh := make(chan error, 1)
	go func() {
		refreshErrCh <- st.Refresh(ctx)
	}()

	// Wait 20ms into refresh, then publish concurrent real-time messages (retained: false)
	time.Sleep(20 * time.Millisecond)

	// Real-time job progress update (progress 95%)
	job1Realtime := `{"id":"job-1","machine_id":"mach-1","status":"running","title":"Job 1","progress":95}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-1/jobs/job-1/status", []byte(job1Realtime), false)

	// Real-time new job
	jobNewRealtime := `{"id":"job-new","machine_id":"mach-1","status":"running","title":"Job New","progress":0}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-1/jobs/job-new/status", []byte(jobNewRealtime), false)

	// Real-time log entry
	logPayload := `{"job_id":"job-1","machine_id":"mach-1","level":"info","message":"realtime progress checkpoint"}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-1/jobs/job-1/logs", []byte(logPayload), false)

	// Wait for refresh to finish
	if err := <-refreshErrCh; err != nil {
		t.Fatalf("Refresh returned unexpected error: %v", err)
	}

	// 1. Verify live store contains both machines and both jobs
	machines, err := st.ListMachines(ctx)
	if err != nil || len(machines) != 2 {
		t.Fatalf("expected 2 machines after refresh, got %+v", machines)
	}

	jobs, err := st.ListJobs(ctx)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("expected 2 jobs after refresh, got %+v", jobs)
	}

	var j1 *model.Job
	for i := range jobs {
		if jobs[i].ID == "job-1" {
			j1 = &jobs[i]
		}
	}
	if j1 == nil || j1.Progress != 95 {
		t.Fatalf("expected job-1 to have progress 95 from real-time update, got: %+v", j1)
	}

	// 2. Verify logs for job-1 preserved the real-time log entry
	logs, err := st.GetLogs(ctx, "job-1", 10)
	if err != nil || len(logs) == 0 {
		t.Fatalf("expected logs for job-1 to contain real-time log, got: %+v (err: %v)", logs, err)
	}
	if !strings.Contains(logs[len(logs)-1].Message, "realtime progress checkpoint") {
		t.Fatalf("expected log entry to contain message, got: %+v", logs[len(logs)-1])
	}

	// 3. Verify subscriber received real-time events
	receivedEvents := make([]model.Event, 0)
	drainDone := false
	for !drainDone {
		select {
		case ev := <-subCh:
			receivedEvents = append(receivedEvents, ev)
		case <-time.After(50 * time.Millisecond):
			drainDone = true
		}
	}

	hasJ1Update := false
	hasJNew := false
	hasLog := false
	for _, ev := range receivedEvents {
		if ev.Type == model.EventJobUpdated && ev.Job != nil {
			if ev.Job.ID == "job-1" && ev.Job.Progress == 95 {
				hasJ1Update = true
			}
			if ev.Job.ID == "job-new" {
				hasJNew = true
			}
		}
		if ev.Type == model.EventLogAdded && ev.Log != nil && ev.Log.JobID == "job-1" {
			hasLog = true
		}
	}
	if !hasJ1Update {
		t.Errorf("expected subscriber to receive EventJobUpdated for job-1 (progress 95)")
	}
	if !hasJNew {
		t.Errorf("expected subscriber to receive EventJobUpdated for job-new")
	}
	if !hasLog {
		t.Errorf("expected subscriber to receive EventLogAdded for job-1 log")
	}
}

func TestMQTTStore_Refresh_ConcurrentRealtimeMessages_FailedRefresh(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 100 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// Initial store state
	mach1Init := `{"id":"mach-base","name":"mach-base","online":true}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-base/status", []byte(mach1Init), true)

	job1Init := `{"id":"job-base","machine_id":"mach-base","status":"running","title":"Base Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-base/jobs/job-base/status", []byte(job1Init), true)

	subCh, err := st.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// Broker has a stale retained node that should be discarded when refresh fails
	fakeClient.Publish("agentpulse/v1/machines/mach-broker-stale/status", 1, true, []byte(`{"id":"mach-broker-stale","online":true}`))

	ctx, cancel := context.WithCancel(context.Background())

	refreshErrCh := make(chan error, 1)
	go func() {
		refreshErrCh <- st.Refresh(ctx)
	}()

	// Wait 20ms into refresh
	time.Sleep(20 * time.Millisecond)

	// Deliver concurrent real-time message (retained: false)
	realtimeJob := `{"id":"job-realtime-important","machine_id":"mach-base","status":"running","title":"Crucial Realtime Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-base/jobs/job-realtime-important/status", []byte(realtimeJob), false)

	// Cancel refresh
	cancel()

	refreshErr := <-refreshErrCh
	if refreshErr == nil {
		t.Fatalf("expected Refresh to fail on cancel, got nil")
	}

	// Verify live store state:
	// 1. mach-broker-stale MUST NOT be present (staging discarded!)
	// 2. job-realtime-important MUST be present (real-time message was merged and NOT lost!)
	checkCtx := context.Background()
	machines, err := st.ListMachines(checkCtx)
	if err != nil {
		t.Fatalf("ListMachines error: %v", err)
	}
	for _, m := range machines {
		if m.ID == "mach-broker-stale" {
			t.Fatalf("mach-broker-stale should not be present after failed refresh")
		}
	}

	jCrucial, err := st.GetJob(checkCtx, "job-realtime-important")
	if err != nil || jCrucial == nil {
		t.Fatalf("job-realtime-important must not be lost after failed refresh: %v", err)
	}

	// 3. Subscriber should have received the event for job-realtime-important
	receivedRealtime := false
	drainDone := false
	for !drainDone {
		select {
		case ev := <-subCh:
			if ev.Type == model.EventJobUpdated && ev.Job != nil && ev.Job.ID == "job-realtime-important" {
				receivedRealtime = true
			}
		case <-time.After(50 * time.Millisecond):
			drainDone = true
		}
	}
	if !receivedRealtime {
		t.Errorf("expected subscriber to receive EventJobUpdated for job-realtime-important after failed refresh replay")
	}
}

// Regression test for SUBACK 0x80 rejection on all topics.
// Reproduces ops isolation test FAIL: broker returns SUBACK with code 0x80, token.Error() is nil,
// semi-finished staging (containing injected qa-partial) must NOT replace live cache.
// Live cache must be retained, staging discarded, and concurrent real-time events must not be lost.
func TestMQTTStore_Refresh_BrokerRejection_SUBACK80_AllTopics(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 50 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Initial store state before refresh
	machOld := `{"id":"mach-old","name":"mach-old","online":true,"job_count":1}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-old/status", []byte(machOld), true)

	jobOld := `{"id":"job-old","machine_id":"mach-old","status":"running","title":"Existing Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-old/jobs/job-old/status", []byte(jobOld), true)

	// Verify initial store state
	machines, err := st.ListMachines(ctx)
	if err != nil || len(machines) != 1 || machines[0].ID != "mach-old" {
		t.Fatalf("initial machines mismatch: %+v", machines)
	}

	// Subscribe to live events
	subCh, err := st.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	// Configure broker to return SUBACK 0x80 rejection for all subscribed topic filters.
	// In Paho, token.Error() remains nil, but token.Result() returns 0x80 for each topic.
	fakeClient.SetSubscribeResults(map[string]byte{
		mqttstore.TopicMachinesStatus:           0x80,
		mqttstore.TopicJobsStatus:               0x80,
		mqttstore.TopicJobsLogs:                 0x80,
		mqttstore.TopicAnnounceWildcard:         0x80,
		mqttstore.TopicMachinesAnnounceWildcard: 0x80,
	})

	// While staging is active inside SubscribeMultiple:
	// 1. Deliver retained qa-partial (staging snapshot injection)
	// 2. Deliver concurrent real-time job-realtime event (retained: false)
	injectedAll := false
	fakeClient.SetOnSubscribeMultiple(func() {
		if injectedAll {
			return
		}
		injectedAll = true
		// qa-partial is a broker retained message injected during the refresh window
		qaPartial := `{"id":"qa-partial","name":"qa-partial","online":true,"job_count":0}`
		fakeClient.Deliver("agentpulse/v1/machines/qa-partial/status", []byte(qaPartial), true)

		// Concurrent real-time message (not retained)
		realtimeJob := `{"id":"job-concurrent-realtime","machine_id":"mach-old","status":"running","title":"Concurrent Realtime Job"}`
		fakeClient.Deliver("agentpulse/v1/machines/mach-old/jobs/job-concurrent-realtime/status", []byte(realtimeJob), false)
	})

	// Refresh must fail with error because broker rejected the subscription with SUBACK 0x80
	refreshErr := st.Refresh(ctx)
	if refreshErr == nil {
		t.Fatalf("expected Refresh to fail when broker returns SUBACK 0x80, got nil")
	}
	if !strings.Contains(refreshErr.Error(), "0x80") {
		t.Errorf("expected error message to mention rejection code 0x80, got: %v", refreshErr)
	}

	// Verify live store state after failed refresh:
	// 1. Original machine (mach-old) MUST remain intact!
	// 2. Staging machine (qa-partial) MUST NOT be present (staging discarded, no semi-finished commit)!
	machinesAfter, err := st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines error: %v", err)
	}
	hasOld := false
	hasQAPartial := false
	for _, m := range machinesAfter {
		if m.ID == "mach-old" {
			hasOld = true
		}
		if m.ID == "qa-partial" {
			hasQAPartial = true
		}
	}
	if !hasOld {
		t.Errorf("mach-old must remain intact in cache after rejected refresh")
	}
	if hasQAPartial {
		t.Fatalf("CRITICAL BUG: qa-partial from semi-finished staging was falsely committed into live cache!")
	}

	// 3. Concurrent real-time message MUST NOT be lost!
	jRealtime, err := st.GetJob(ctx, "job-concurrent-realtime")
	if err != nil || jRealtime == nil {
		t.Fatalf("job-concurrent-realtime was lost during failed refresh: %v", err)
	}
	jOld, err := st.GetJob(ctx, "job-old")
	if err != nil || jOld == nil {
		t.Fatalf("job-old was lost during failed refresh: %v", err)
	}

	// 4. Subscriber should receive event for concurrent real-time message
	receivedRealtime := false
	drainDone := false
	for !drainDone {
		select {
		case ev := <-subCh:
			if ev.Type == model.EventJobUpdated && ev.Job != nil && ev.Job.ID == "job-concurrent-realtime" {
				receivedRealtime = true
			}
		case <-time.After(50 * time.Millisecond):
			drainDone = true
		}
	}
	if !receivedRealtime {
		t.Errorf("expected subscriber to receive event for job-concurrent-realtime")
	}
}

// Regression test for partial subscription rejection:
// When one topic (TopicJobsStatus) is rejected (0x80) while others succeed (QoS 1),
// Refresh must fail and NOT commit a partial snapshot.
func TestMQTTStore_Refresh_BrokerRejection_SUBACK80_PartialRejection(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	cfg.SettleTimeout = 50 * time.Millisecond

	st, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Initial store state
	machKeep := `{"id":"mach-keep","name":"mach-keep","online":true}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-keep/status", []byte(machKeep), true)

	jobKeep := `{"id":"job-keep","machine_id":"mach-keep","status":"done","title":"Keep Job"}`
	fakeClient.Deliver("agentpulse/v1/machines/mach-keep/jobs/job-keep/status", []byte(jobKeep), true)

	// Configure partial rejection:
	// Machines topic is granted QoS 1, but Jobs status topic is rejected with 0x80
	fakeClient.SetSubscribeResults(map[string]byte{
		mqttstore.TopicJobsStatus: 0x80,
	})

	injectedPartial := false
	fakeClient.SetOnSubscribeMultiple(func() {
		if injectedPartial {
			return
		}
		injectedPartial = true
		// qa-partial is delivered to the accepted TopicMachinesStatus
		qaPartial := `{"id":"qa-partial","name":"qa-partial","online":true}`
		fakeClient.Deliver("agentpulse/v1/machines/qa-partial/status", []byte(qaPartial), true)
	})

	// Refresh must fail due to partial rejection
	refreshErr := st.Refresh(ctx)
	if refreshErr == nil {
		t.Fatalf("expected Refresh to fail on partial subscription rejection, got nil")
	}
	if !strings.Contains(refreshErr.Error(), "0x80") {
		t.Errorf("expected error to mention rejection code 0x80, got: %v", refreshErr)
	}

	// Verify staging was discarded and NOT committed:
	// mach-keep must still exist, qa-partial must NOT exist, job-keep must still exist
	machines, err := st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines error: %v", err)
	}
	for _, m := range machines {
		if m.ID == "qa-partial" {
			t.Fatalf("CRITICAL BUG: qa-partial committed on partial rejection!")
		}
	}
	if len(machines) != 1 || machines[0].ID != "mach-keep" {
		t.Errorf("expected only mach-keep, got: %+v", machines)
	}

	job, err := st.GetJob(ctx, "job-keep")
	if err != nil || job == nil {
		t.Errorf("expected job-keep to remain intact, got err=%v", err)
	}
}

func TestMQTTStore_NewWithClient_BrokerRejection_SUBACK80(t *testing.T) {
	fakeClient := mqttstore.NewFakeMQTTClient()
	fakeClient.SetSubscribeResults(map[string]byte{
		mqttstore.TopicMachinesStatus: 0x80,
	})

	cfg := mqttstore.DefaultConfig()
	_, err := mqttstore.NewWithClient(cfg, fakeClient)
	if err == nil {
		t.Fatalf("expected NewWithClient to fail when initial subscription is rejected with SUBACK 0x80, got nil")
	}
	if !strings.Contains(err.Error(), "0x80") {
		t.Errorf("expected error to mention 0x80 rejection code, got: %v", err)
	}
}

func TestCheckSubscribeResult_Unit(t *testing.T) {
	// 1. Nil token
	if err := mqttstore.CheckSubscribeResult(nil); err != nil {
		t.Errorf("expected nil error for nil token, got %v", err)
	}

	// 2. Token without resultGetter
	fakeBase := mqttstore.NewFakeMQTTClient()
	if err := mqttstore.CheckSubscribeResult(fakeBase.Connect()); err != nil {
		t.Errorf("expected nil error for non-subscribe token, got %v", err)
	}
}

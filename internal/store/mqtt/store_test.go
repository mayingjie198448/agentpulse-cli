package mqtt_test

import (
	"context"
	"fmt"
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

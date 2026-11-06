package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
)

var _ store.Store = (*Store)(nil)

// Store implements store.Store backed by a remote MQTT broker.
// It subscribes to machine status, job status, and log streams,
// maintaining in-memory caches and bounded log ring buffers.
type Store struct {
	cfg         Config
	client      paho.Client
	mu          sync.RWMutex
	machines    map[string]*model.Machine
	jobs        map[string]*model.Job
	logs        map[string]*logRingBuffer
	announces   map[string]*model.MachineAnnounce
	subscribers map[chan model.Event]struct{}
	closeCh     chan struct{}
	closed      bool
}

// New initializes a new MQTT Store, connects to the configured broker,
// and subscribes to the AgentPulse v1 topic trees.
func New(cfg Config) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	s := &Store{
		cfg:         cfg,
		machines:    make(map[string]*model.Machine),
		jobs:        make(map[string]*model.Job),
		logs:        make(map[string]*logRingBuffer),
		announces:   make(map[string]*model.MachineAnnounce),
		subscribers: make(map[chan model.Event]struct{}),
		closeCh:     make(chan struct{}),
	}

	opts := paho.NewClientOptions()
	if err := ApplyClientOptions(opts, cfg); err != nil {
		return nil, fmt.Errorf("MQTT client options error: %w", err)
	}
	opts.SetAutoReconnect(true)
	opts.SetCleanSession(true)

	// Resubscribe automatically upon reconnection
	opts.SetOnConnectHandler(func(c paho.Client) {
		filters := map[string]byte{
			TopicMachinesStatus:           1,
			TopicJobsStatus:               1,
			TopicJobsLogs:                 0,
			TopicAnnounceWildcard:         1,
			TopicMachinesAnnounceWildcard: 1,
		}
		c.SubscribeMultiple(filters, s.onMQTTMessage)
	})

	client := paho.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(cfg.Timeout) {
		return nil, fmt.Errorf("MQTT connection timed out to %s", cfg.BrokerURL)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("MQTT connection failed to %s: %w", cfg.BrokerURL, err)
	}

	s.client = client

	// Explicit initial subscription
	filters := map[string]byte{
		TopicMachinesStatus:           1,
		TopicJobsStatus:               1,
		TopicJobsLogs:                 0,
		TopicAnnounceWildcard:         1,
		TopicMachinesAnnounceWildcard: 1,
	}
	subToken := client.SubscribeMultiple(filters, s.onMQTTMessage)
	if !subToken.WaitTimeout(cfg.Timeout) {
		client.Disconnect(100)
		return nil, fmt.Errorf("MQTT subscription timed out")
	}
	if err := subToken.Error(); err != nil {
		client.Disconnect(100)
		return nil, fmt.Errorf("MQTT subscription failed: %w", err)
	}

	go s.runHeartbeatChecker()

	// Cold-start retained settle per docs/mqtt-protocol.md §5.1:
	// Wait briefly for retained messages from broker to settle so one-shot queries (list/logs)
	// are not falsely empty immediately after connect.
	if cfg.SettleTimeout > 0 {
		time.Sleep(cfg.SettleTimeout)
	}
	s.checkStaleMachines()

	return s, nil
}

// NewWithClient creates a Store using an existing or mock paho.Client.
// Useful for unit testing without spinning up a live network broker.
func NewWithClient(cfg Config, client paho.Client) (*Store, error) {
	if cfg.BrokerURL == "" {
		cfg.BrokerURL = "mock://client"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	s := &Store{
		cfg:         cfg,
		client:      client,
		machines:    make(map[string]*model.Machine),
		jobs:        make(map[string]*model.Job),
		logs:        make(map[string]*logRingBuffer),
		announces:   make(map[string]*model.MachineAnnounce),
		subscribers: make(map[chan model.Event]struct{}),
		closeCh:     make(chan struct{}),
	}

	if client != nil {
		filters := map[string]byte{
			TopicMachinesStatus:           1,
			TopicJobsStatus:               1,
			TopicJobsLogs:                 0,
			TopicAnnounceWildcard:         1,
			TopicMachinesAnnounceWildcard: 1,
		}
		token := client.SubscribeMultiple(filters, s.onMQTTMessage)
		if token != nil {
			token.WaitTimeout(cfg.Timeout)
		}
	}

	go s.runHeartbeatChecker()

	if cfg.SettleTimeout > 0 {
		time.Sleep(cfg.SettleTimeout)
	}
	s.checkStaleMachines()

	return s, nil
}

func (s *Store) onMQTTMessage(_ paho.Client, msg paho.Message) {
	s.HandleMessage(msg.Topic(), msg.Payload())
}

// HandleMessage parses and handles incoming MQTT messages for machine status,
// job status, and job logs. Exported for tests and mock message injection.
func (s *Store) HandleMessage(topic string, payload []byte) {
	pt := parseTopic(topic)
	if pt.typ == topicUnknown {
		return
	}

	switch pt.typ {
	case topicMachineStatus:
		s.handleMachineStatus(pt, payload)
	case topicJobStatus:
		s.handleJobStatus(pt, payload)
	case topicJobLog:
		s.handleJobLog(pt, payload)
	case topicAnnounce:
		s.handleAnnounce(pt, payload)
	}
}

func (s *Store) handleMachineStatus(pt parsedTopic, payload []byte) {
	if len(bytes.TrimSpace(payload)) == 0 {
		// Empty payload is an MQTT clear-retained tombstone
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		deletedMachine, exists := s.machines[pt.machineID]
		delete(s.machines, pt.machineID)
		s.mu.Unlock()

		if exists && deletedMachine != nil {
			delCopy := *deletedMachine
			delCopy.Online = false
			s.broadcast(model.Event{
				Type:      model.EventMachineUpdated,
				Machine:   &delCopy,
				Timestamp: time.Now(),
			})
		}
		return
	}

	var m model.Machine
	if err := json.Unmarshal(payload, &m); err != nil {
		return
	}
	if m.ID == "" {
		m.ID = pt.machineID
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	if m.LastHeartbeat.IsZero() {
		m.LastHeartbeat = time.Now()
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	stored := m
	s.machines[m.ID] = &stored
	s.mu.Unlock()

	broadcastMachine := m
	s.broadcast(model.Event{
		Type:      model.EventMachineUpdated,
		Machine:   &broadcastMachine,
		Timestamp: time.Now(),
	})
}

func (s *Store) handleJobStatus(pt parsedTopic, payload []byte) {
	if len(bytes.TrimSpace(payload)) == 0 {
		// Empty payload is an MQTT clear-retained tombstone per docs/mqtt-protocol.md
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		deletedJob, exists := s.jobs[pt.jobID]
		delete(s.jobs, pt.jobID)
		delete(s.logs, pt.jobID)
		s.mu.Unlock()

		if exists && deletedJob != nil {
			s.broadcast(model.Event{
				Type:      model.EventJobDeleted,
				Job:       deletedJob,
				Timestamp: time.Now(),
			})
		}
		return
	}

	var j model.Job
	if err := json.Unmarshal(payload, &j); err != nil {
		return
	}
	if j.ID == "" {
		j.ID = pt.jobID
	}
	if j.MachineID == "" {
		j.MachineID = pt.machineID
	}
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = time.Now()
	}
	if j.StartedAt.IsZero() {
		j.StartedAt = j.UpdatedAt
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	stored := j
	s.jobs[j.ID] = &stored
	s.mu.Unlock()

	broadcastJob := j
	s.broadcast(model.Event{
		Type:      model.EventJobUpdated,
		Job:       &broadcastJob,
		Timestamp: time.Now(),
	})
}

func (s *Store) handleJobLog(pt parsedTopic, payload []byte) {
	var entry model.LogEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		// Fallback for raw text lines
		entry = model.LogEntry{
			Timestamp: time.Now(),
			JobID:     pt.jobID,
			MachineID: pt.machineID,
			Level:     model.LevelInfo,
			Message:   string(payload),
		}
	} else {
		if entry.JobID == "" {
			entry.JobID = pt.jobID
		}
		if entry.MachineID == "" {
			entry.MachineID = pt.machineID
		}
		if entry.Timestamp.IsZero() {
			entry.Timestamp = time.Now()
		}
		if entry.Level == "" {
			entry.Level = model.LevelInfo
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	if entry.Engine == "" {
		if j, ok := s.jobs[entry.JobID]; ok {
			entry.Engine = j.Engine
		}
	}

	rb, ok := s.logs[entry.JobID]
	if !ok {
		rb = newLogRingBuffer(s.cfg.LogCap)
		s.logs[entry.JobID] = rb
	}
	rb.Append(entry)
	s.mu.Unlock()

	broadcastLog := entry
	s.broadcast(model.Event{
		Type:      model.EventLogAdded,
		Log:       &broadcastLog,
		Timestamp: time.Now(),
	})
}

func (s *Store) handleAnnounce(pt parsedTopic, payload []byte) {
	if len(bytes.TrimSpace(payload)) == 0 {
		// Empty payload is an MQTT clear-retained tombstone
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		delete(s.announces, pt.machineID)
		s.mu.Unlock()
		return
	}

	var ann model.MachineAnnounce
	if err := json.Unmarshal(payload, &ann); err != nil {
		return
	}
	if ann.MachineID == "" {
		ann.MachineID = pt.machineID
	}
	if ann.TS.IsZero() {
		ann.TS = time.Now().UTC()
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	// Conflict resolution per 0.5.0 spec:
	// "同 machine_id 冲突比 rev（或 ts）；他机只读合并，不写进自己的 config"
	existing, exists := s.announces[ann.MachineID]
	if exists && existing != nil {
		if ann.Rev < existing.Rev {
			s.mu.Unlock()
			return
		}
		if ann.Rev == existing.Rev && !ann.TS.After(existing.TS) {
			s.mu.Unlock()
			return
		}
	}

	stored := ann
	s.announces[ann.MachineID] = &stored
	s.mu.Unlock()

	broadcastAnn := ann
	s.broadcast(model.Event{
		Type:      model.EventAnnounceUpdated,
		Announce:  &broadcastAnn,
		Timestamp: time.Now(),
	})
}

// ListMachines returns all cached machines, ordered online first, then by ID.
func (s *Store) ListMachines(ctx context.Context) ([]model.Machine, error) {
	s.checkStaleMachines()

	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]model.Machine, 0, len(s.machines))
	for _, m := range s.machines {
		list = append(list, *m)
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].Online != list[j].Online {
			return list[i].Online
		}
		return list[i].ID < list[j].ID
	})

	return list, nil
}

// GetMachine returns a single machine by ID.
func (s *Store) GetMachine(ctx context.Context, id string) (*model.Machine, error) {
	s.checkStaleMachines()

	s.mu.RLock()
	defer s.mu.RUnlock()

	if m, ok := s.machines[id]; ok {
		cp := *m
		return &cp, nil
	}
	return nil, fmt.Errorf("machine not found: %s", id)
}

// ListJobs returns all cached jobs, ordered by execution status priority then ID.
func (s *Store) ListJobs(ctx context.Context) ([]model.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]model.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, *j)
	}

	statusWeight := func(st model.JobStatus) int {
		switch st {
		case model.StatusRunning:
			return 0
		case model.StatusQueued:
			return 1
		case model.StatusFailed:
			return 2
		case model.StatusDone:
			return 3
		default:
			return 4
		}
	}

	sort.Slice(list, func(i, j int) bool {
		wi, wj := statusWeight(list[i].Status), statusWeight(list[j].Status)
		if wi != wj {
			return wi < wj
		}
		return list[i].ID < list[j].ID
	})

	return list, nil
}

// GetJob returns a specific job by its ID.
func (s *Store) GetJob(ctx context.Context, id string) (*model.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if j, ok := s.jobs[id]; ok {
		cp := *j
		return &cp, nil
	}
	return nil, fmt.Errorf("job not found: %s", id)
}

// DeleteJob removes a job and its logs from the store, publishes an empty retained payload
// to the job status topic to clear broker retained state, and cleans up local artifacts.
// If the job is currently running, it refuses deletion with a clear Chinese error.
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("store is closed")
	}

	job, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("job not found: %s", id)
	}

	if job.Status == model.StatusRunning {
		s.mu.Unlock()
		return fmt.Errorf("任务 %s 正在运行中，无法删除", id)
	}

	machineID := job.MachineID
	deletedJob := *job

	// Remove from in-memory cache
	delete(s.jobs, id)
	delete(s.logs, id)
	client := s.client
	s.mu.Unlock()

	// Publish empty retained payload on agentpulse/v1/machines/{machine_id}/jobs/{job_id}/status
	// to clear broker retained status per MQTT spec
	if client != nil && client.IsConnected() {
		topic := JobStatusTopic(machineID, id)
		token := client.Publish(topic, 1, true, []byte{})
		if !token.WaitTimeout(s.cfg.Timeout) {
			return fmt.Errorf("MQTT publish clear-retained timed out for %s", topic)
		}
		if err := token.Error(); err != nil {
			return fmt.Errorf("MQTT publish clear-retained failed: %w", err)
		}
	}

	// Clean up AgentPulse-produced local artifacts for this job only
	_ = store.CleanJobArtifacts(id, "")

	// Broadcast EventJobDeleted to local subscribers
	s.broadcast(model.Event{
		Type:      model.EventJobDeleted,
		Job:       &deletedJob,
		Timestamp: time.Now(),
	})

	return nil
}

// GetLogs returns the most recent logs for a specific job from the ring buffer.
func (s *Store) GetLogs(ctx context.Context, jobID string, limit int) ([]model.LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if rb, ok := s.logs[jobID]; ok {
		return rb.Slice(limit), nil
	}
	if _, ok := s.jobs[jobID]; ok {
		return []model.LogEntry{}, nil
	}
	return nil, fmt.Errorf("no logs found for job: %s", jobID)
}

// ListAnnounces returns all discovered machine announcements across the network.
func (s *Store) ListAnnounces(ctx context.Context) ([]model.MachineAnnounce, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.MachineAnnounce, 0, len(s.announces))
	for _, ann := range s.announces {
		result = append(result, *ann)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MachineID < result[j].MachineID
	})
	return result, nil
}

// GetAnnounce returns the announcement for a specific machine ID.
func (s *Store) GetAnnounce(ctx context.Context, machineID string) (*model.MachineAnnounce, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ann, ok := s.announces[machineID]
	if !ok || ann == nil {
		return nil, fmt.Errorf("announcement not found for machine %q", machineID)
	}
	cp := *ann
	return &cp, nil
}

// PublishAnnounce publishes an announcement payload to agentpulse/v1/{machine_id}/announce.
func PublishAnnounce(client paho.Client, ann model.MachineAnnounce) error {
	topic := MachineAnnounceTopic(ann.MachineID)
	data, err := json.Marshal(ann)
	if err != nil {
		return fmt.Errorf("failed to marshal announce: %w", err)
	}
	token := client.Publish(topic, 1, true, data)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("publish announce to %s timed out", topic)
	}
	return token.Error()
}

// Subscribe returns an event channel receiving real-time state changes and logs.
// The channel has a bounded buffer to prevent slow consumers from backing up MQTT handlers.
func (s *Store) Subscribe(ctx context.Context) (<-chan model.Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("store is closed")
	}

	ch := make(chan model.Event, s.cfg.BufferCap)
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			if _, exists := s.subscribers[ch]; exists {
				delete(s.subscribers, ch)
				close(ch)
			}
			s.mu.Unlock()
		case <-s.closeCh:
		}
	}()

	return ch, nil
}

// broadcast dispatches an event to all subscribers with non-blocking backpressure mitigation.
// It deep-copies event pointers so subscribers never alias map-owned objects or each other.
func (s *Store) broadcast(ev model.Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return
	}

	for ch := range s.subscribers {
		// Deep-copy event payload (value copy then take address) for each subscriber
		eventCopy := ev
		if ev.Machine != nil {
			mCopy := *ev.Machine
			eventCopy.Machine = &mCopy
		}
		if ev.Job != nil {
			jCopy := *ev.Job
			eventCopy.Job = &jCopy
		}
		if ev.Log != nil {
			lCopy := *ev.Log
			eventCopy.Log = &lCopy
		}

		select {
		case ch <- eventCopy:
		default:
			// Backpressure policy per docs/mqtt-protocol.md §6.3:
			// For high-frequency log events: drop when consumer buffer is full.
			// For critical state updates (machine/job): evict an older item and enqueue state change.
			if eventCopy.Type == model.EventMachineUpdated || eventCopy.Type == model.EventJobUpdated || eventCopy.Type == model.EventJobDeleted {
				select {
				case <-ch: // evict one old event
				default:
				}
				select {
				case ch <- eventCopy:
				default:
				}
			}
		}
	}
}

// Close disconnects MQTT subscriptions and shuts down background checkers.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.closeCh)

	for ch := range s.subscribers {
		close(ch)
	}
	s.subscribers = make(map[chan model.Event]struct{})
	client := s.client
	s.mu.Unlock()

	if client != nil && client.IsConnected() {
		client.Unsubscribe(TopicMachinesStatus, TopicJobsStatus, TopicJobsLogs)
		client.Disconnect(250)
	}

	return nil
}

// IsConnected reports whether the underlying MQTT client is actively connected to the broker.
func (s *Store) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.client == nil {
		return false
	}
	return s.client.IsConnected()
}

func (s *Store) runHeartbeatChecker() {
	s.checkStaleMachines()
	ticker := time.NewTicker(s.cfg.HeartbeatCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			s.checkStaleMachines()
		}
	}
}

// checkStaleMachines marks machines offline if heartbeat exceeds timeout per §6.4.
// It avoids in-place mutation of *m so earlier broadcast pointers are never raced on.
func (s *Store) checkStaleMachines() {
	var expired []*model.Machine
	now := time.Now()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	for id, m := range s.machines {
		if m.Online && now.Sub(m.LastHeartbeat) > s.cfg.HeartbeatTimeout {
			// Do not mutate *m in place as it may still be referenced by an earlier Event pointer.
			// Update via copy into the map.
			updated := *m
			updated.Online = false
			s.machines[id] = &updated

			// Prepare a fresh copy for broadcast
			fresh := updated
			expired = append(expired, &fresh)
		}
	}
	s.mu.Unlock()

	for _, m := range expired {
		s.broadcast(model.Event{
			Type:      model.EventMachineUpdated,
			Machine:   m,
			Timestamp: now,
		})
	}
}

// CheckStaleMachines explicitly runs stale machine inspection (exported for testing).
func (s *Store) CheckStaleMachines() {
	s.checkStaleMachines()
}

// Refresh re-subscribes to MQTT topics, clears stale in-memory state, and settles
// retained messages from the broker per docs/mqtt-protocol.md §5.1.
func (s *Store) Refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("store closed")
	}
	// Clear cached state so records removed or cleared on broker are dropped
	s.machines = make(map[string]*model.Machine)
	s.jobs = make(map[string]*model.Job)
	s.announces = make(map[string]*model.MachineAnnounce)
	s.mu.Unlock()

	if s.client == nil || !s.client.IsConnected() {
		s.checkStaleMachines()
		return nil
	}

	filters := map[string]byte{
		TopicMachinesStatus:           1,
		TopicJobsStatus:               1,
		TopicJobsLogs:                 0,
		TopicAnnounceWildcard:         1,
		TopicMachinesAnnounceWildcard: 1,
	}
	unsubTopics := []string{
		TopicMachinesStatus,
		TopicJobsStatus,
		TopicJobsLogs,
		TopicAnnounceWildcard,
		TopicMachinesAnnounceWildcard,
	}

	if token := s.client.Unsubscribe(unsubTopics...); token != nil {
		token.WaitTimeout(s.cfg.Timeout)
	}

	subToken := s.client.SubscribeMultiple(filters, s.onMQTTMessage)
	if subToken != nil {
		if !subToken.WaitTimeout(s.cfg.Timeout) {
			return fmt.Errorf("MQTT subscription timed out during refresh")
		}
		if err := subToken.Error(); err != nil {
			return fmt.Errorf("MQTT subscription failed during refresh: %w", err)
		}
	}

	settle := s.cfg.SettleTimeout
	if settle <= 0 {
		settle = 200 * time.Millisecond
	}

	select {
	case <-time.After(settle):
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closeCh:
		return fmt.Errorf("store closed")
	}

	s.checkStaleMachines()
	return nil
}

// Settle pauses the caller for the configured duration (or default) to allow retained messages to settle.
func (s *Store) Settle(d time.Duration) {
	if d <= 0 {
		d = s.cfg.SettleTimeout
	}
	if d > 0 {
		time.Sleep(d)
	}
}

// RawMachinePointer returns the pointer stored in the machines map for pointer-aliasing verification in tests.
func (s *Store) RawMachinePointer(id string) *model.Machine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.machines[id]
}

// RawJobPointer returns the pointer stored in the jobs map for pointer-aliasing verification in tests.
func (s *Store) RawJobPointer(id string) *model.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jobs[id]
}

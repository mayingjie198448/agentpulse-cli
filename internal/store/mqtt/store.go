package mqtt

import (
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
		subscribers: make(map[chan model.Event]struct{}),
		closeCh:     make(chan struct{}),
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
	opts.SetCleanSession(true)

	// Resubscribe automatically upon reconnection
	opts.SetOnConnectHandler(func(c paho.Client) {
		filters := map[string]byte{
			TopicMachinesStatus: 1,
			TopicJobsStatus:     1,
			TopicJobsLogs:       0,
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
		TopicMachinesStatus: 1,
		TopicJobsStatus:     1,
		TopicJobsLogs:       0,
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
		subscribers: make(map[chan model.Event]struct{}),
		closeCh:     make(chan struct{}),
	}

	if client != nil {
		filters := map[string]byte{
			TopicMachinesStatus: 1,
			TopicJobsStatus:     1,
			TopicJobsLogs:       0,
		}
		token := client.SubscribeMultiple(filters, s.onMQTTMessage)
		if token != nil {
			token.WaitTimeout(cfg.Timeout)
		}
	}

	go s.runHeartbeatChecker()

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
	}
}

func (s *Store) handleMachineStatus(pt parsedTopic, payload []byte) {
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
	s.machines[m.ID] = &m
	s.mu.Unlock()

	s.broadcast(model.Event{
		Type:      model.EventMachineUpdated,
		Machine:   &m,
		Timestamp: time.Now(),
	})
}

func (s *Store) handleJobStatus(pt parsedTopic, payload []byte) {
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
	s.jobs[j.ID] = &j
	s.mu.Unlock()

	s.broadcast(model.Event{
		Type:      model.EventJobUpdated,
		Job:       &j,
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

	s.broadcast(model.Event{
		Type:      model.EventLogAdded,
		Log:       &entry,
		Timestamp: time.Now(),
	})
}

// ListMachines returns all cached machines, ordered online first, then by ID.
func (s *Store) ListMachines(ctx context.Context) ([]model.Machine, error) {
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
func (s *Store) broadcast(ev model.Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return
	}

	for ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
			// Backpressure policy per docs/mqtt-protocol.md §6.3:
			// For high-frequency log events: drop when consumer buffer is full.
			// For critical state updates (machine/job): evict an older item and enqueue state change.
			if ev.Type == model.EventMachineUpdated || ev.Type == model.EventJobUpdated {
				select {
				case <-ch: // evict one old event
				default:
				}
				select {
				case ch <- ev:
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

func (s *Store) runHeartbeatChecker() {
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
func (s *Store) checkStaleMachines() {
	var expired []*model.Machine
	now := time.Now()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	for _, m := range s.machines {
		if m.Online && now.Sub(m.LastHeartbeat) > s.cfg.HeartbeatTimeout {
			m.Online = false
			cp := *m
			expired = append(expired, &cp)
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

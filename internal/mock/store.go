package mock

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
)

var _ store.Store = (*MockStore)(nil)

// MockStore implements store.Store using in-memory state and simulated live streams.
type MockStore struct {
	mu          sync.RWMutex
	machines    map[string]*model.Machine
	jobs        map[string]*model.Job
	logs        map[string][]model.LogEntry
	announces   map[string]*model.MachineAnnounce
	subscribers map[chan model.Event]struct{}
	stopCh      chan struct{}
	closed      bool
}

// NewMockStore initializes a MockStore populated with predefined machines, jobs, and logs.
func NewMockStore() *MockStore {
	now := time.Now()

	s := &MockStore{
		machines:    make(map[string]*model.Machine),
		jobs:        make(map[string]*model.Job),
		logs:        make(map[string][]model.LogEntry),
		announces:   make(map[string]*model.MachineAnnounce),
		subscribers: make(map[chan model.Event]struct{}),
		stopCh:      make(chan struct{}),
	}

	// 1. Initial Machines: ccdq, mac-mini, home-pc, macbook
	machines := []*model.Machine{
		{
			ID:            "ccdq",
			Name:          "ccdq",
			Online:        true,
			LastHeartbeat: now.Add(-1 * time.Second),
			Platform:      "linux/amd64",
			IP:            "10.0.1.42",
			JobCount:      2,
		},
		{
			ID:            "mac-mini",
			Name:          "mac-mini",
			Online:        true,
			LastHeartbeat: now.Add(-2 * time.Second),
			Platform:      "darwin/arm64",
			IP:            "192.168.1.105",
			JobCount:      2,
		},
		{
			ID:            "home-pc",
			Name:          "home-pc",
			Online:        true,
			LastHeartbeat: now.Add(-3 * time.Second),
			Platform:      "windows/amd64",
			IP:            "192.168.1.180",
			JobCount:      1,
		},
		{
			ID:            "macbook",
			Name:          "macbook",
			Online:        false,
			LastHeartbeat: now.Add(-14 * time.Minute),
			Platform:      "darwin/arm64",
			IP:            "192.168.1.201",
			JobCount:      1,
		},
	}

	for _, m := range machines {
		s.machines[m.ID] = m
	}

	// 1.1 Initial Machine Announces (0.5.0 spec)
	s.announces["ccdq"] = &model.MachineAnnounce{
		V:         1,
		MachineID: "ccdq",
		TS:        now,
		Rev:       1,
		Hostname:  "ccdq-srv",
		OS:        "linux/amd64",
		Agents: []model.AgentAnnounce{
			{ID: "agy", Alias: []string{"agy"}, Ok: true, Detail: "/usr/local/bin/agy"},
			{ID: "opencode", Alias: []string{"op", "opencode"}, Ok: true, Detail: "/usr/local/bin/opencode"},
		},
	}
	s.announces["mac-mini"] = &model.MachineAnnounce{
		V:         1,
		MachineID: "mac-mini",
		TS:        now,
		Rev:       1,
		Hostname:  "mac-mini.local",
		OS:        "darwin/arm64",
		Agents: []model.AgentAnnounce{
			{ID: "agy", Alias: []string{"agy"}, Ok: true, Detail: "/opt/homebrew/bin/agy"},
			{ID: "opencode", Alias: []string{"op", "opencode"}, Ok: true, Detail: "/opt/homebrew/bin/opencode"},
			{ID: "codex", Alias: []string{"codex"}, Ok: false, Detail: "not found in PATH"},
		},
	}
	s.announces["home-pc"] = &model.MachineAnnounce{
		V:         1,
		MachineID: "home-pc",
		TS:        now,
		Rev:       1,
		Hostname:  "HOME-DESKTOP",
		OS:        "windows/amd64",
		Agents: []model.AgentAnnounce{
			{ID: "opencode", Alias: []string{"op", "opencode"}, Ok: true, Detail: "C:\\bin\\opencode.exe"},
			{ID: "codex", Alias: []string{"codex"}, Ok: true, Detail: "C:\\bin\\codex.exe"},
		},
	}
	s.announces["macbook"] = &model.MachineAnnounce{
		V:         1,
		MachineID: "macbook",
		TS:        now.Add(-14 * time.Minute),
		Rev:       1,
		Hostname:  "MacBook-Air.local",
		OS:        "darwin/arm64",
		Agents: []model.AgentAnnounce{
			{ID: "agy", Alias: []string{"agy"}, Ok: true, Detail: "/usr/local/bin/agy"},
		},
	}

	// 2. Initial Jobs across machines and engines
	jobs := []*model.Job{
		{
			ID:          "job-agy-01",
			MachineID:   "ccdq",
			Engine:      model.EngineAgy,
			Status:      model.StatusRunning,
			Progress:    68.0,
			Cwd:         "/workspace/payment-service",
			Command:     "agy --goal 'Refactor auth middleware and token refresh'",
			Title:       "Auth Middleware Refactor",
			StartedAt:   now.Add(-12 * time.Minute),
			UpdatedAt:   now,
			DurationStr: "12m 14s",
		},
		{
			ID:          "job-opencode-02",
			MachineID:   "ccdq",
			Engine:      model.EngineOpenCode,
			Status:      model.StatusDone,
			Progress:    100.0,
			Cwd:         "/workspace/docs-sync",
			Command:     "opencode sync --target docs/v2",
			Title:       "API Documentation Sync",
			StartedAt:   now.Add(-45 * time.Minute),
			UpdatedAt:   now.Add(-15 * time.Minute),
			DurationStr: "29m 40s",
		},
		{
			ID:          "job-agy-03",
			MachineID:   "mac-mini",
			Engine:      model.EngineAgy,
			Status:      model.StatusFailed,
			Progress:    42.0,
			Cwd:         "/Users/mini/agent-tasks/eval-bench",
			Command:     "agy eval --suite benchmarks/concurrency",
			Title:       "Concurrency Benchmarks",
			StartedAt:   now.Add(-30 * time.Minute),
			UpdatedAt:   now.Add(-18 * time.Minute),
			DurationStr: "11m 45s",
			Error:       "exit status 1: test worker deadlock detected in suite 4",
		},
		{
			ID:          "job-opencode-04",
			MachineID:   "mac-mini",
			Engine:      model.EngineOpenCode,
			Status:      model.StatusRunning,
			Progress:    24.0,
			Cwd:         "/Users/mini/Repos/rag-agent",
			Command:     "opencode refactor --module embedding_pipeline",
			Title:       "RAG Embedding Pipeline",
			StartedAt:   now.Add(-5 * time.Minute),
			UpdatedAt:   now,
			DurationStr: "5m 22s",
		},
		{
			ID:          "job-agy-05",
			MachineID:   "home-pc",
			Engine:      model.EngineAgy,
			Status:      model.StatusRunning,
			Progress:    88.0,
			Cwd:         `D:\projects\code-gen-runner`,
			Command:     "agy run --prompt 'Optimize AST codegen throughput'",
			Title:       "AST Codegen Throughput",
			StartedAt:   now.Add(-20 * time.Minute),
			UpdatedAt:   now,
			DurationStr: "20m 05s",
		},
		{
			ID:          "job-opencode-06",
			MachineID:   "macbook",
			Engine:      model.EngineOpenCode,
			Status:      model.StatusDone,
			Progress:    100.0,
			Cwd:         "/Users/mbp/infra/k8s-agent",
			Command:     "opencode plan --manifest manifests/ingress.yaml",
			Title:       "Ingress Manifest Plan",
			StartedAt:   now.Add(-80 * time.Minute),
			UpdatedAt:   now.Add(-55 * time.Minute),
			DurationStr: "24m 10s",
		},
	}

	for _, j := range jobs {
		s.jobs[j.ID] = j
	}

	// 3. Prepopulate some logs for each job
	s.seedInitialLogs(now)

	// Start background simulation ticker
	go s.runSimulation()

	return s
}

func (s *MockStore) seedInitialLogs(now time.Time) {
	s.logs["job-agy-01"] = []model.LogEntry{
		{Timestamp: now.Add(-10 * time.Minute), JobID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Level: model.LevelInfo, Message: "Initialized agent runtime with model gemini-3.8-flash"},
		{Timestamp: now.Add(-9 * time.Minute), JobID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Level: model.LevelAgent, Message: "[planner] Reading workspace files in /workspace/payment-service"},
		{Timestamp: now.Add(-8 * time.Minute), JobID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Level: model.LevelTool, Message: "invoke_subagent: Codebase Researcher (analyzing auth handler)"},
		{Timestamp: now.Add(-6 * time.Minute), JobID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Level: model.LevelInfo, Message: "Identified 4 entrypoints for JWT token renewal"},
		{Timestamp: now.Add(-4 * time.Minute), JobID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Level: model.LevelAgent, Message: "Modifying internal/middleware/jwt.go: added sliding window TTL"},
		{Timestamp: now.Add(-2 * time.Minute), JobID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Level: model.LevelTool, Message: "run_command: go test ./internal/middleware -v"},
		{Timestamp: now.Add(-1 * time.Minute), JobID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Level: model.LevelInfo, Message: "PASS: TestJWTValidation and TestTokenExpiration (42ms)"},
	}

	s.logs["job-opencode-02"] = []model.LogEntry{
		{Timestamp: now.Add(-40 * time.Minute), JobID: "job-opencode-02", MachineID: "ccdq", Engine: model.EngineOpenCode, Level: model.LevelInfo, Message: "Session opened: syncing 18 endpoints to OpenAPI 3.1"},
		{Timestamp: now.Add(-30 * time.Minute), JobID: "job-opencode-02", MachineID: "ccdq", Engine: model.EngineOpenCode, Level: model.LevelAgent, Message: "Parsed Go docstrings into markdown schema documentation"},
		{Timestamp: now.Add(-15 * time.Minute), JobID: "job-opencode-02", MachineID: "ccdq", Engine: model.EngineOpenCode, Level: model.LevelInfo, Message: "Successfully emitted docs/v2/swagger.json and changelog.md"},
	}

	s.logs["job-agy-03"] = []model.LogEntry{
		{Timestamp: now.Add(-28 * time.Minute), JobID: "job-agy-03", MachineID: "mac-mini", Engine: model.EngineAgy, Level: model.LevelInfo, Message: "Spawning benchmark runner across 8 threads"},
		{Timestamp: now.Add(-22 * time.Minute), JobID: "job-agy-03", MachineID: "mac-mini", Engine: model.EngineAgy, Level: model.LevelWarn, Message: "Thread 3 mutex lock contention exceeds 2500ms"},
		{Timestamp: now.Add(-18 * time.Minute), JobID: "job-agy-03", MachineID: "mac-mini", Engine: model.EngineAgy, Level: model.LevelError, Message: "DEADLOCK in benchmark suite 4: workers [1, 3] waiting on resource sync.Locker"},
	}

	s.logs["job-opencode-04"] = []model.LogEntry{
		{Timestamp: now.Add(-4 * time.Minute), JobID: "job-opencode-04", MachineID: "mac-mini", Engine: model.EngineOpenCode, Level: model.LevelInfo, Message: "Indexing vector store schema in /Users/mini/Repos/rag-agent"},
		{Timestamp: now.Add(-2 * time.Minute), JobID: "job-opencode-04", MachineID: "mac-mini", Engine: model.EngineOpenCode, Level: model.LevelAgent, Message: "Refactoring cosine similarity batch calculator"},
	}

	s.logs["job-agy-05"] = []model.LogEntry{
		{Timestamp: now.Add(-15 * time.Minute), JobID: "job-agy-05", MachineID: "home-pc", Engine: model.EngineAgy, Level: model.LevelInfo, Message: "Warmup LLM context for D:\\projects\\code-gen-runner"},
		{Timestamp: now.Add(-5 * time.Minute), JobID: "job-agy-05", MachineID: "home-pc", Engine: model.EngineAgy, Level: model.LevelAgent, Message: "Benchmarking AST traversal speed before/after inline caching"},
	}

	s.logs["job-opencode-06"] = []model.LogEntry{
		{Timestamp: now.Add(-75 * time.Minute), JobID: "job-opencode-06", MachineID: "macbook", Engine: model.EngineOpenCode, Level: model.LevelInfo, Message: "Validating k8s ingress yaml syntax against schema v1.28"},
		{Timestamp: now.Add(-55 * time.Minute), JobID: "job-opencode-06", MachineID: "macbook", Engine: model.EngineOpenCode, Level: model.LevelInfo, Message: "Dry run completed: all ingress paths routed successfully"},
	}
}

// ListMachines returns all mock machines.
func (s *MockStore) ListMachines(ctx context.Context) ([]model.Machine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.Machine, 0, len(s.machines))
	// Return in consistent order: ccdq, mac-mini, home-pc, macbook
	order := []string{"ccdq", "mac-mini", "home-pc", "macbook"}
	for _, id := range order {
		if m, ok := s.machines[id]; ok {
			result = append(result, *m)
		}
	}
	return result, nil
}

// GetMachine returns a mock machine by id.
func (s *MockStore) GetMachine(ctx context.Context, id string) (*model.Machine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if m, ok := s.machines[id]; ok {
		cp := *m
		return &cp, nil
	}
	return nil, fmt.Errorf("machine not found: %s", id)
}

// ListJobs returns all mock jobs.
func (s *MockStore) ListJobs(ctx context.Context) ([]model.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.Job, 0, len(s.jobs))
	order := []string{"job-agy-01", "job-opencode-02", "job-agy-03", "job-opencode-04", "job-agy-05", "job-opencode-06"}
	for _, id := range order {
		if j, ok := s.jobs[id]; ok {
			result = append(result, *j)
		}
	}
	return result, nil
}

// GetJob returns a mock job by id.
func (s *MockStore) GetJob(ctx context.Context, id string) (*model.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if j, ok := s.jobs[id]; ok {
		cp := *j
		return &cp, nil
	}
	return nil, fmt.Errorf("job not found: %s", id)
}

// GetLogs returns mock logs for a job.
func (s *MockStore) GetLogs(ctx context.Context, jobID string, limit int) ([]model.LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, ok := s.logs[jobID]
	if !ok {
		return nil, fmt.Errorf("no logs found for job: %s", jobID)
	}

	if limit <= 0 || limit >= len(entries) {
		copied := make([]model.LogEntry, len(entries))
		copy(copied, entries)
		return copied, nil
	}

	start := len(entries) - limit
	copied := make([]model.LogEntry, limit)
	copy(copied, entries[start:])
	return copied, nil
}

// ListAnnounces returns all mock machine announcements.
func (s *MockStore) ListAnnounces(ctx context.Context) ([]model.MachineAnnounce, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.MachineAnnounce, 0, len(s.announces))
	order := []string{"ccdq", "mac-mini", "home-pc", "macbook"}
	for _, id := range order {
		if ann, ok := s.announces[id]; ok {
			result = append(result, *ann)
		}
	}
	return result, nil
}

// GetAnnounce returns a mock machine announcement by machineID.
func (s *MockStore) GetAnnounce(ctx context.Context, machineID string) (*model.MachineAnnounce, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if ann, ok := s.announces[machineID]; ok {
		cp := *ann
		return &cp, nil
	}
	return nil, fmt.Errorf("announcement not found for machine: %s", machineID)
}

// Subscribe opens a channel for simulated events.
func (s *MockStore) Subscribe(ctx context.Context) (<-chan model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, fmt.Errorf("store is closed")
	}

	ch := make(chan model.Event, 64)
	s.subscribers[ch] = struct{}{}

	// Handle cleanup when ctx is done
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
	}()

	return ch, nil
}

// Close stops the mock simulation loop.
func (s *MockStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	close(s.stopCh)

	for ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, ch)
	}
	return nil
}

func (s *MockStore) broadcastLocked(ev model.Event) {
	for ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
			// Non-blocking drop if consumer buffer is full
		}
	}
}

// Simulated dynamic log messages for streaming
var agyLogTemplates = []string{
	"[planner] Decomposing subtask: optimize cache layer",
	"[tool] run_command: git diff --stat",
	"[agent] Verified AST syntax and lint check: 0 warnings",
	"[tool] invoke_subagent: Unit Test Generator",
	"[planner] Formulating patch proposal for auth session check",
	"[tool] edit_file: patched internal/session/store.go (+12, -3)",
	"[agent] Executed test suite: 18 passed, 0 failed in 340ms",
	"[artifact] Saved memory snapshot to brain/artifacts/step_audit.json",
}

var openCodeLogTemplates = []string{
	"[opencode::session] Synchronizing workspace buffer...",
	"[opencode::llm] Ingestion completed: 1,240 tokens generated",
	"[opencode::patch] Applying fuzzy match to config/routes.ts",
	"[opencode::tool] Executing: npm test -- --runInBand",
	"[opencode::eval] Coverage report: statements 89.4%, branches 82.1%",
	"[opencode::exec] Building target distribution artifacts",
	"[opencode::agent] Auto-commit candidate created: 'feat(core): enhance token bucket'",
}

// runSimulation runs periodic ticks to advance progress, update heartbeats, and emit logs.
func (s *MockStore) runSimulation() {
	heartbeatTicker := time.NewTicker(1000 * time.Millisecond)
	progressTicker := time.NewTicker(700 * time.Millisecond)
	logTicker := time.NewTicker(400 * time.Millisecond)
	defer heartbeatTicker.Stop()
	defer progressTicker.Stop()
	defer logTicker.Stop()

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		case <-s.stopCh:
			return

		case t := <-heartbeatTicker.C:
			// Update heartbeats for online machines
			s.mu.Lock()
			for _, m := range s.machines {
				if m.Online {
					m.LastHeartbeat = t
					cp := *m
					s.broadcastLocked(model.Event{
						Type:      model.EventMachineUpdated,
						Machine:   &cp,
						Timestamp: t,
					})
				}
			}
			s.mu.Unlock()

		case t := <-progressTicker.C:
			// Advance progress of running jobs
			s.mu.Lock()
			runningJobIDs := []string{"job-agy-01", "job-opencode-04", "job-agy-05"}
			for _, jid := range runningJobIDs {
				j, ok := s.jobs[jid]
				if !ok || j.Status != model.StatusRunning {
					continue
				}

				// Increment progress
				increment := float64(r.Intn(3) + 1)
				j.Progress += increment
				if j.Progress >= 100.0 {
					j.Progress = 100.0
					j.Status = model.StatusDone
				}
				j.UpdatedAt = t

				cp := *j
				s.broadcastLocked(model.Event{
					Type:      model.EventJobUpdated,
					Job:       &cp,
					Timestamp: t,
				})
			}
			s.mu.Unlock()

		case t := <-logTicker.C:
			// Generate a simulated log message for an active job
			s.mu.Lock()
			runningJobs := []*model.Job{}
			for _, j := range s.jobs {
				if j.Status == model.StatusRunning {
					runningJobs = append(runningJobs, j)
				}
			}

			if len(runningJobs) > 0 {
				chosenJob := runningJobs[r.Intn(len(runningJobs))]
				var msg string
				var lvl model.LogLevel = model.LevelInfo

				if chosenJob.Engine == model.EngineAgy {
					msg = agyLogTemplates[r.Intn(len(agyLogTemplates))]
					if r.Float32() < 0.3 {
						lvl = model.LevelAgent
					} else if r.Float32() < 0.5 {
						lvl = model.LevelTool
					}
				} else {
					msg = openCodeLogTemplates[r.Intn(len(openCodeLogTemplates))]
					if r.Float32() < 0.3 {
						lvl = model.LevelAgent
					}
				}

				entry := model.LogEntry{
					Timestamp: t,
					JobID:     chosenJob.ID,
					MachineID: chosenJob.MachineID,
					Engine:    chosenJob.Engine,
					Level:     lvl,
					Message:   msg,
				}

				s.logs[chosenJob.ID] = append(s.logs[chosenJob.ID], entry)
				s.broadcastLocked(model.Event{
					Type:      model.EventLogAdded,
					Log:       &entry,
					Timestamp: t,
				})
			}
			s.mu.Unlock()
		}
	}
}

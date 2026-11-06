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

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

func boolPtr(b bool) *bool {
	return &b
}

func getLastPublished(mock *mockMQTTClient, topic string) *publishedMsg {
	all := mock.GetPublished()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Topic == topic {
			return &all[i]
		}
	}
	return nil
}

func TestTokenizeCommandLine(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    "agy -p 'fix database connection'",
			expected: []string{"agy", "-p", "fix database connection"},
		},
		{
			input:    `opencode run --prompt "refactor store"`,
			expected: []string{"opencode", "run", "--prompt", "refactor store"},
		},
		{
			input:    `agy -p line1\012line2`,
			expected: []string{"agy", "-p", "line1\nline2"},
		},
		{
			input:    "  /usr/local/bin/agy   --flag   val  ",
			expected: []string{"/usr/local/bin/agy", "--flag", "val"},
		},
	}

	for _, tt := range tests {
		got := wrapper.TokenizeCommandLine(tt.input)
		if len(got) != len(tt.expected) {
			t.Fatalf("TokenizeCommandLine(%q) returned %d tokens, expected %d: %v", tt.input, len(got), len(tt.expected), got)
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("token %d mismatch: got %q, expected %q", i, got[i], tt.expected[i])
			}
		}
	}
}

func TestMatchAgentProcess(t *testing.T) {
	agents := []config.AgentConfig{
		{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: boolPtr(true),
		},
		{
			ID:      "opencode",
			Alias:   []string{"op", "opencode"},
			Enabled: boolPtr(true),
		},
		{
			ID:      "disabled-agent",
			Alias:   []string{"dis"},
			Enabled: boolPtr(false),
		},
		{
			ID:      "custom-tool",
			Path:    "/opt/bin/my-tool",
			Enabled: boolPtr(true),
		},
	}

	selfPID := 1000
	dispatchPIDs := map[int]bool{2000: true}
	isDispatch := func(pid int) bool { return dispatchPIDs[pid] }

	tests := []struct {
		name      string
		proc      wrapper.ProcessInfo
		wantMatch bool
		wantAgID  string
	}{
		{
			name: "valid bare agy",
			proc: wrapper.ProcessInfo{
				PID:  1234,
				PPID: 1,
				Args: []string{"/usr/local/bin/agy", "-p", "Fix auth bug"},
			},
			wantMatch: true,
			wantAgID:  "agy",
		},
		{
			name: "valid bare opencode alias op",
			proc: wrapper.ProcessInfo{
				PID:  1235,
				PPID: 1,
				Args: []string{"op", "-p", "Refactor module"},
			},
			wantMatch: true,
			wantAgID:  "opencode",
		},
		{
			name: "custom tool path match",
			proc: wrapper.ProcessInfo{
				PID:  1236,
				PPID: 1,
				Args: []string{"/opt/bin/my-tool", "--run"},
			},
			wantMatch: true,
			wantAgID:  "custom-tool",
		},
		{
			name: "disabled agent ignored",
			proc: wrapper.ProcessInfo{
				PID:  1237,
				PPID: 1,
				Args: []string{"disabled-agent", "task"},
			},
			wantMatch: false,
		},
		{
			name: "self PID ignored",
			proc: wrapper.ProcessInfo{
				PID:  selfPID,
				PPID: 1,
				Args: []string{"agy", "-p", "test"},
			},
			wantMatch: false,
		},
		{
			name: "child of self ignored",
			proc: wrapper.ProcessInfo{
				PID:  1238,
				PPID: selfPID,
				Args: []string{"agy", "-p", "test"},
			},
			wantMatch: false,
		},
		{
			name: "tracked dispatch PID ignored",
			proc: wrapper.ProcessInfo{
				PID:  2000,
				PPID: 1,
				Args: []string{"agy", "-p", "dispatch task"},
			},
			wantMatch: false,
		},
		{
			name: "grep ignored",
			proc: wrapper.ProcessInfo{
				PID:  1239,
				PPID: 1,
				Args: []string{"grep", "agy", "main.go"},
			},
			wantMatch: false,
		},
		{
			name: "vim ignored",
			proc: wrapper.ProcessInfo{
				PID:  1240,
				PPID: 1,
				Args: []string{"vim", "internal/agy.go"},
			},
			wantMatch: false,
		},
		{
			name: "agentpulse dispatch ignored",
			proc: wrapper.ProcessInfo{
				PID:  1241,
				PPID: 1,
				Args: []string{"agentpulse", "dispatch", "agy", "--", "-p", "test"},
			},
			wantMatch: false,
		},
		{
			name: "test binary ignored",
			proc: wrapper.ProcessInfo{
				PID:  1242,
				PPID: 1,
				Args: []string{"wrapper.test", "-test.run", "TestSomething"},
			},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag, matched := wrapper.MatchAgentProcess(tt.proc, agents, isDispatch, selfPID)
			if matched != tt.wantMatch {
				t.Fatalf("MatchAgentProcess: got match %v, want %v", matched, tt.wantMatch)
			}
			if matched && ag.ID != tt.wantAgID {
				t.Errorf("matched agent ID mismatch: got %q, want %q", ag.ID, tt.wantAgID)
			}
		})
	}
}

func TestGenerateAttachJobID(t *testing.T) {
	st := time.Unix(1700000000, 0)
	id1 := wrapper.GenerateAttachJobID("agy", 54321, st)
	expected := "job-attach-agy-54321-1700000000"
	if id1 != expected {
		t.Errorf("expected %q, got %q", expected, id1)
	}

	id2 := wrapper.GenerateAttachJobID("opencode", 1234, time.Time{})
	expected2 := "job-attach-opencode-1234"
	if id2 != expected2 {
		t.Errorf("expected %q, got %q", expected2, id2)
	}
}

func TestAttachManager_Lifecycle_Mock(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	logDir := filepath.Join(tmpDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("failed to create log dir: %v", err)
	}

	testPID := 98765
	logFilePath := filepath.Join(logDir, "cli-20261106_120000.log")
	initialLogContent := fmt.Sprintf("I1106 12:00:00.000000 1 server.go:2048] Starting language server process with pid %d\nI1106 12:00:01.000000 1 server.go:1000] Initializing agent environment...\n", testPID)
	if err := os.WriteFile(logFilePath, []byte(initialLogContent), 0644); err != nil {
		t.Fatalf("failed to write mock log: %v", err)
	}

	var procAliveMu sync.Mutex
	procAlive := true
	isAlive := func(pid int) bool {
		procAliveMu.Lock()
		defer procAliveMu.Unlock()
		return procAlive
	}

	startTime := time.Now().Add(-10 * time.Second).UTC()
	mockProcs := []wrapper.ProcessInfo{
		{
			PID:       testPID,
			PPID:      1,
			StartTime: startTime,
			Args:      []string{"agy", "-p", "Fix user login race condition"},
			Cwd:       "/tmp/project",
		},
	}

	mockLister := func(ctx context.Context) ([]wrapper.ProcessInfo, error) {
		procAliveMu.Lock()
		alive := procAlive
		procAliveMu.Unlock()
		if alive {
			return mockProcs, nil
		}
		return nil, nil
	}

	var countChanges int
	var countMu sync.Mutex
	onCountChange := func() {
		countMu.Lock()
		countChanges++
		countMu.Unlock()
	}

	agents := []config.AgentConfig{
		{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: boolPtr(true),
		},
	}

	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:          "test-mac-box",
		MQTTClient:         mockClient,
		Agents:             agents,
		ScanInterval:       50 * time.Millisecond,
		ProcessLister:      mockLister,
		IsProcessAlive:     isAlive,
		ProcessExitChecker: func(pid int) (int, bool) { return 0, true },
		AgyLogDir:          logDir,
		Quiet:              true,
		OnJobCountChange:   onCountChange,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(ctx)

	expectedJobID := wrapper.GenerateAttachJobID("agy", testPID, startTime)
	statusTopic := mqtt.JobStatusTopic("test-mac-box", expectedJobID)
	logsTopic := mqtt.JobLogsTopic("test-mac-box", expectedJobID)

	// 1. Wait for initial running status to be published
	var initialJob model.Job
	for i := 0; i < 20; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			if err := json.Unmarshal(pub.Payload, &initialJob); err == nil && initialJob.Status == model.StatusRunning {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if initialJob.Status != model.StatusRunning {
		t.Fatalf("expected initial job status running, got %q", initialJob.Status)
	}
	if initialJob.Title != "Fix user login race condition" {
		t.Errorf("expected title from prompt flag, got %q", initialJob.Title)
	}
	if mgr.ActiveCount() != 1 {
		t.Errorf("expected active count 1, got %d", mgr.ActiveCount())
	}

	// 2. Check initial log entry published
	var logPub *publishedMsg
	for i := 0; i < 20; i++ {
		pub := getLastPublished(mockClient, logsTopic)
		if pub != nil {
			logPub = pub
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if logPub == nil {
		t.Fatalf("expected log entry published to %s", logsTopic)
	}

	// 3. Append log line with progress update to mock log file
	f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("failed to open log file for append: %v", err)
	}
	_, _ = f.WriteString("I1106 12:00:02.000000 1 task.go:50] Progress: 50%\n")
	_ = f.Close()

	// Wait for progress update to reflect in status publication
	var updatedJob model.Job
	for i := 0; i < 20; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			_ = json.Unmarshal(pub.Payload, &updatedJob)
			if updatedJob.Progress >= 50.0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if updatedJob.Progress < 50.0 {
		t.Errorf("expected progress >= 50.0, got %f", updatedJob.Progress)
	}

	// 4. Simulate process exit
	procAliveMu.Lock()
	procAlive = false
	procAliveMu.Unlock()

	// Wait for final done status
	var finalJob model.Job
	for i := 0; i < 30; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			_ = json.Unmarshal(pub.Payload, &finalJob)
			if finalJob.Status == model.StatusDone {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if finalJob.Status != model.StatusDone {
		t.Fatalf("expected final status done, got %q", finalJob.Status)
	}
	if finalJob.Progress != 100.0 {
		t.Errorf("expected final progress 100.0, got %f", finalJob.Progress)
	}
	if mgr.ActiveCount() != 0 {
		t.Errorf("expected active count 0 after process exit, got %d", mgr.ActiveCount())
	}

	// 5. Verify alert was published to notifications topic
	alertPub := getLastPublished(mockClient, "agentpulse/v1/notifications/alerts")
	if alertPub == nil {
		t.Fatalf("expected completion alert published to agentpulse/v1/notifications/alerts")
	}

	mgr.Stop()
}

func TestAttachManager_DisabledAgentNotScooped(t *testing.T) {
	mockClient := newMockMQTTClient()
	agents := []config.AgentConfig{
		{
			ID:      "opencode",
			Alias:   []string{"opencode"},
			Enabled: boolPtr(false),
		},
	}

	mockProcs := []wrapper.ProcessInfo{
		{
			PID:  77777,
			PPID: 1,
			Args: []string{"opencode", "run", "--prompt", "disabled task"},
		},
	}

	mockLister := func(ctx context.Context) ([]wrapper.ProcessInfo, error) {
		return mockProcs, nil
	}

	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:     "test-box",
		MQTTClient:    mockClient,
		Agents:        agents,
		ScanInterval:  30 * time.Millisecond,
		ProcessLister: mockLister,
		IsProcessAlive: func(pid int) bool {
			return true
		},
		Quiet: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	if mgr.ActiveCount() != 0 {
		t.Errorf("expected 0 active jobs for disabled agent, got %d", mgr.ActiveCount())
	}

	mgr.Stop()
}

func TestListener_WithBareAttach(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "status.json")

	testPID := 88888
	startTime := time.Now().Add(-5 * time.Second).UTC()
	mockProcs := []wrapper.ProcessInfo{
		{
			PID:       testPID,
			PPID:      1,
			StartTime: startTime,
			Args:      []string{"agy", "-p", "Bare listener task"},
		},
	}

	var procAliveMu sync.Mutex
	procAlive := true
	isAlive := func(pid int) bool {
		procAliveMu.Lock()
		defer procAliveMu.Unlock()
		return procAlive
	}

	mockLister := func(ctx context.Context) ([]wrapper.ProcessInfo, error) {
		procAliveMu.Lock()
		alive := procAlive
		procAliveMu.Unlock()
		if alive {
			return mockProcs, nil
		}
		return nil, nil
	}

	agents := []config.AgentConfig{
		{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: boolPtr(true),
		},
	}

	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:          "listener-node",
		MachineName:        "Listener Node",
		HeartbeatInterval:  100 * time.Millisecond,
		StatusFile:         statusFile,
		MQTTClient:         mockClient,
		Quiet:              true,
		Agents:             agents,
		AttachScanInterval: 40 * time.Millisecond,
		ProcessLister:      mockLister,
		IsProcessAlive:     isAlive,
		ProcessExitChecker: func(pid int) (int, bool) { return 0, true },
	})
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- l.Run(ctx)
	}()

	// Wait for listener to discover bare process and reflect in machine heartbeat
	jobID := wrapper.GenerateAttachJobID("agy", testPID, startTime)
	jobTopic := mqtt.JobStatusTopic("listener-node", jobID)
	machTopic := mqtt.MachineStatusTopic("listener-node")

	var jobPub *publishedMsg
	for i := 0; i < 30; i++ {
		if pub := getLastPublished(mockClient, jobTopic); pub != nil {
			jobPub = pub
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if jobPub == nil {
		t.Fatalf("expected job status published on %s", jobTopic)
	}

	// Verify machine status reflects job_count >= 1
	var mach model.Machine
	for i := 0; i < 20; i++ {
		if pub := getLastPublished(mockClient, machTopic); pub != nil {
			_ = json.Unmarshal(pub.Payload, &mach)
			if mach.JobCount >= 1 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mach.JobCount < 1 {
		t.Errorf("expected machine job_count >= 1 with bare attach, got %d", mach.JobCount)
	}

	// Terminate the process
	procAliveMu.Lock()
	procAlive = false
	procAliveMu.Unlock()

	// Wait for job to finalize
	var finalJob model.Job
	for i := 0; i < 30; i++ {
		if pub := getLastPublished(mockClient, jobTopic); pub != nil {
			_ = json.Unmarshal(pub.Payload, &finalJob)
			if finalJob.Status == model.StatusDone {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if finalJob.Status != model.StatusDone {
		t.Errorf("expected job done, got %q", finalJob.Status)
	}

	l.Stop()
	cancel()
	<-errCh
}

// TestAttachManager_ExitNonZero_MarkedFailed verifies that when a bare agent process exits
// with a non-zero exit code (e.g. exit=7), AttachManager marks the job as FAILED (status != DONE)
// and records the non-zero exit code in the Error field, preventing false positive completion.
func TestAttachManager_ExitNonZero_MarkedFailed(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	logDir := filepath.Join(tmpDir, "log")
	_ = os.MkdirAll(logDir, 0755)

	testPID := 88801
	var procAliveMu sync.Mutex
	procAlive := true
	isAlive := func(pid int) bool {
		procAliveMu.Lock()
		defer procAliveMu.Unlock()
		return procAlive
	}

	startTime := time.Now().Add(-5 * time.Second).UTC()
	mockProcs := []wrapper.ProcessInfo{
		{
			PID:       testPID,
			PPID:      1,
			StartTime: startTime,
			Args:      []string{"agy", "-p", "Failing task simulation"},
			Cwd:       "/tmp/project",
		},
	}

	mockLister := func(ctx context.Context) ([]wrapper.ProcessInfo, error) {
		procAliveMu.Lock()
		alive := procAlive
		procAliveMu.Unlock()
		if alive {
			return mockProcs, nil
		}
		return nil, nil
	}

	agents := []config.AgentConfig{
		{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: boolPtr(true),
		},
	}

	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "test-box-fail",
		MQTTClient:     mockClient,
		Agents:         agents,
		ScanInterval:   40 * time.Millisecond,
		ProcessLister:  mockLister,
		IsProcessAlive: isAlive,
		ProcessExitChecker: func(pid int) (int, bool) {
			// Simulate exit code 7 (exact reproduction case)
			return 7, true
		},
		AgyLogDir: logDir,
		Quiet:     true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(ctx)

	expectedJobID := wrapper.GenerateAttachJobID("agy", testPID, startTime)
	statusTopic := mqtt.JobStatusTopic("test-box-fail", expectedJobID)

	// 1. Wait for initial running status
	var runningJob model.Job
	for i := 0; i < 20; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			if err := json.Unmarshal(pub.Payload, &runningJob); err == nil && runningJob.Status == model.StatusRunning {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	if runningJob.Status != model.StatusRunning {
		t.Fatalf("expected running job, got %q", runningJob.Status)
	}

	// 2. Terminate the process (exit=7)
	procAliveMu.Lock()
	procAlive = false
	procAliveMu.Unlock()

	// 3. Wait for finalized job status
	var finalJob model.Job
	for i := 0; i < 30; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			_ = json.Unmarshal(pub.Payload, &finalJob)
			if finalJob.Status == model.StatusFailed {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}

	if finalJob.Status == model.StatusDone {
		t.Fatalf("regression failed: job with exit=7 must NOT be marked DONE!")
	}
	if finalJob.Status != model.StatusFailed {
		t.Fatalf("expected job status failed, got %q", finalJob.Status)
	}
	if !strings.Contains(finalJob.Error, "退出码 7") {
		t.Errorf("expected error to mention exit code 7, got %q", finalJob.Error)
	}
	if finalJob.Progress == 100.0 {
		t.Errorf("failed job progress must not be blindly set to 100.0, got %f", finalJob.Progress)
	}

	mgr.Stop()
}

// TestAttachManager_ExitUnknown_MarkedFailed verifies that when a process disappears
// and its exit code cannot be determined (and no reliable success evidence exists),
// AttachManager conservatively marks it FAILED (status != DONE) and sets a clear
// Chinese explanation rather than treating uncertainty as DONE.
func TestAttachManager_ExitUnknown_MarkedFailed(t *testing.T) {
	mockClient := newMockMQTTClient()
	tmpDir := t.TempDir()
	logDir := filepath.Join(tmpDir, "log")
	_ = os.MkdirAll(logDir, 0755)

	testPID := 88802
	var procAliveMu sync.Mutex
	procAlive := true
	isAlive := func(pid int) bool {
		procAliveMu.Lock()
		defer procAliveMu.Unlock()
		return procAlive
	}

	startTime := time.Now().Add(-5 * time.Second).UTC()
	mockProcs := []wrapper.ProcessInfo{
		{
			PID:       testPID,
			PPID:      1,
			StartTime: startTime,
			Args:      []string{"agy", "-p", "Unobservable termination task"},
			Cwd:       "/tmp/project",
		},
	}

	mockLister := func(ctx context.Context) ([]wrapper.ProcessInfo, error) {
		procAliveMu.Lock()
		alive := procAlive
		procAliveMu.Unlock()
		if alive {
			return mockProcs, nil
		}
		return nil, nil
	}

	agents := []config.AgentConfig{
		{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: boolPtr(true),
		},
	}

	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "test-box-unknown",
		MQTTClient:     mockClient,
		Agents:         agents,
		ScanInterval:   40 * time.Millisecond,
		ProcessLister:  mockLister,
		IsProcessAlive: isAlive,
		// No ProcessExitChecker configured -> exit code cannot be determined
		ProcessExitChecker: nil,
		AgyLogDir:          logDir,
		Quiet:              true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(ctx)

	expectedJobID := wrapper.GenerateAttachJobID("agy", testPID, startTime)
	statusTopic := mqtt.JobStatusTopic("test-box-unknown", expectedJobID)

	// 1. Wait for running
	var runningJob model.Job
	for i := 0; i < 20; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			if err := json.Unmarshal(pub.Payload, &runningJob); err == nil && runningJob.Status == model.StatusRunning {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	if runningJob.Status != model.StatusRunning {
		t.Fatalf("expected running job, got %q", runningJob.Status)
	}

	// 2. Process disappears
	procAliveMu.Lock()
	procAlive = false
	procAliveMu.Unlock()

	// 3. Wait for finalized job status
	var finalJob model.Job
	for i := 0; i < 30; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			_ = json.Unmarshal(pub.Payload, &finalJob)
			if finalJob.Status == model.StatusFailed {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}

	if finalJob.Status == model.StatusDone {
		t.Fatalf("regression failed: job with undetermined exit code must NOT be marked DONE!")
	}
	if finalJob.Status != model.StatusFailed {
		t.Fatalf("expected job status failed, got %q", finalJob.Status)
	}
	if !strings.Contains(finalJob.Error, "无法判定退出码") {
		t.Errorf("expected error to state 无法判定退出码, got %q", finalJob.Error)
	}

	mgr.Stop()
}

// TestAttachManager_PIDAllowlist_Isolation verifies that injecting PIDAllowlist
// completely isolates tests from scanning or attaching to other running processes
// on the developer host machine (such as parallel coding processes PID 3445/3456).
func TestAttachManager_PIDAllowlist_Isolation(t *testing.T) {
	mockClient := newMockMQTTClient()

	mockProcs := []wrapper.ProcessInfo{
		{
			PID:  12345,
			PPID: 1,
			Args: []string{"agy", "-p", "Allowed test task"},
		},
		{
			PID:  3445,
			PPID: 1,
			Args: []string{"agy", "-p", "Real developer secret prompt sk-SECRET-1"},
		},
		{
			PID:  3456,
			PPID: 1,
			Args: []string{"agy", "-p", "Another developer session sk-SECRET-2"},
		},
	}

	mockLister := func(ctx context.Context) ([]wrapper.ProcessInfo, error) {
		return mockProcs, nil
	}

	agents := []config.AgentConfig{
		{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: boolPtr(true),
		},
	}

	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:     "isolated-node",
		MQTTClient:    mockClient,
		Agents:        agents,
		ScanInterval:  40 * time.Millisecond,
		ProcessLister: mockLister,
		IsProcessAlive: func(pid int) bool {
			return true
		},
		PIDAllowlist: []int{12345}, // Only allow PID 12345!
		Quiet:        true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(ctx)

	// Wait for scan to execute
	time.Sleep(150 * time.Millisecond)

	// Verify only PID 12345 was attached
	if mgr.ActiveCount() != 1 {
		t.Fatalf("expected exactly 1 active job, got %d", mgr.ActiveCount())
	}

	// Verify that PID 3445 and 3456 were NEVER published
	allPubs := mockClient.GetPublished()
	for _, pub := range allPubs {
		payloadStr := string(pub.Payload)
		if strings.Contains(payloadStr, "3445") || strings.Contains(payloadStr, "3456") {
			t.Fatalf("forbidden: leaked unallowed PID 3445/3456 into published MQTT payload: %s", payloadStr)
		}
		if strings.Contains(payloadStr, "SECRET") {
			t.Fatalf("forbidden: leaked prompt into published MQTT payload: %s", payloadStr)
		}
	}

	mgr.Stop()
}

// TestAttachManager_ContextCancelled_Interrupted verifies that when monitoring
// is stopped/cancelled externally, the job is marked FAILED with an interruption error.
func TestAttachManager_ContextCancelled_Interrupted(t *testing.T) {
	mockClient := newMockMQTTClient()
	testPID := 88803
	startTime := time.Now().Add(-2 * time.Second).UTC()

	mockProcs := []wrapper.ProcessInfo{
		{
			PID:       testPID,
			PPID:      1,
			StartTime: startTime,
			Args:      []string{"agy", "-p", "Interrupted task"},
		},
	}

	agents := []config.AgentConfig{
		{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: boolPtr(true),
		},
	}

	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "test-box-cancel",
		MQTTClient:     mockClient,
		Agents:         agents,
		ScanInterval:   40 * time.Millisecond,
		ProcessLister:  func(ctx context.Context) ([]wrapper.ProcessInfo, error) { return mockProcs, nil },
		IsProcessAlive: func(pid int) bool { return true }, // Still alive
		Quiet:          true,
	})

	ctx, cancel := context.WithCancel(context.Background())

	mgr.Start(ctx)

	expectedJobID := wrapper.GenerateAttachJobID("agy", testPID, startTime)
	statusTopic := mqtt.JobStatusTopic("test-box-cancel", expectedJobID)

	// Wait for running status
	for i := 0; i < 20; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			var j model.Job
			if err := json.Unmarshal(pub.Payload, &j); err == nil && j.Status == model.StatusRunning {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}

	// Cancel context to simulate daemon shutdown
	cancel()

	// Wait for finalized status
	var finalJob model.Job
	for i := 0; i < 30; i++ {
		pub := getLastPublished(mockClient, statusTopic)
		if pub != nil {
			_ = json.Unmarshal(pub.Payload, &finalJob)
			if finalJob.Status == model.StatusFailed {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}

	if finalJob.Status != model.StatusFailed {
		t.Fatalf("expected job status failed after cancellation, got %q", finalJob.Status)
	}
	if !strings.Contains(finalJob.Error, "中断") && !strings.Contains(finalJob.Error, "停止") {
		t.Errorf("expected error to indicate interruption, got %q", finalJob.Error)
	}

	mgr.Stop()
}

func TestParsePIDAllowlist(t *testing.T) {
	tests := []struct {
		input    string
		expected []int
	}{
		{"100, 200,300", []int{100, 200, 300}},
		{"400 500;600\n700", []int{400, 500, 600, 700}},
		{"", nil},
		{"abc, -1, 0, 800", []int{800}},
	}

	for _, tt := range tests {
		got := wrapper.ParsePIDAllowlist(tt.input)
		if len(got) != len(tt.expected) {
			t.Fatalf("ParsePIDAllowlist(%q) returned %d items, want %d", tt.input, len(got), len(tt.expected))
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("ParsePIDAllowlist(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}

package wrapper_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

const (
	synthCwd       = "/synthetic/attach-lifecycle"
	synthTitle     = "synthetic lifecycle"
	synthExitCode  = 7
	unknownExitErr = "无法判定退出码（依据不足，非正常可观测退出）"
	interruptErr   = "进程监控被中断或停止"
	nonZeroExitErr = "进程非正常退出 (退出码 7)"
)

// synthAgent is an in-memory process record. Nothing here signals, scans, or
// waits on a real PID.
type synthAgent struct {
	mu           sync.Mutex
	pid          int
	alive        bool
	start        time.Time
	cwdCalls     []int
	startCalls   []int
	aliveCalls   []int
	checkerCalls []int
}

func newSynthAgent(pid int) *synthAgent {
	return &synthAgent{
		pid:   pid,
		alive: true,
		start: time.Unix(1700000000, 0).UTC(),
	}
}

func (s *synthAgent) setAlive(v bool) {
	s.mu.Lock()
	s.alive = v
	s.mu.Unlock()
}

func (s *synthAgent) isAlive(pid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aliveCalls = append(s.aliveCalls, pid)
	return pid == s.pid && s.alive
}

func (s *synthAgent) stillAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

func (s *synthAgent) cwd(pid int) string {
	s.mu.Lock()
	s.cwdCalls = append(s.cwdCalls, pid)
	s.mu.Unlock()
	if pid != s.pid {
		return ""
	}
	return synthCwd
}

func (s *synthAgent) startTime(pid int) time.Time {
	s.mu.Lock()
	s.startCalls = append(s.startCalls, pid)
	s.mu.Unlock()
	if pid != s.pid {
		return time.Time{}
	}
	return s.start
}

func (s *synthAgent) noteChecker(pid int) {
	s.mu.Lock()
	s.checkerCalls = append(s.checkerCalls, pid)
	s.mu.Unlock()
}

func (s *synthAgent) checkerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.checkerCalls)
}

func (s *synthAgent) callsOnlyOurPID(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pid := range append(append(append([]int{}, s.cwdCalls...), s.startCalls...), append(s.aliveCalls, s.checkerCalls...)...) {
		if pid != s.pid {
			t.Fatalf("seam was called for pid %d, only synthetic pid %d is allowed", pid, s.pid)
		}
	}
	if len(s.cwdCalls) == 0 || len(s.startCalls) == 0 {
		t.Fatalf("cwd resolver calls=%d start resolver calls=%d, both seams must be used", len(s.cwdCalls), len(s.startCalls))
	}
}

type synthRun struct {
	mgr    *wrapper.AttachManager
	client *mockMQTTClient
	agent  *synthAgent
	cancel context.CancelFunc
	topic  string
}

func startSynth(t *testing.T, pid int, checker func(int) (int, bool)) *synthRun {
	t.Helper()
	agent := newSynthAgent(pid)
	client := newMockMQTTClient()
	logDir := t.TempDir()
	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "synth-box",
		MQTTClient:     client,
		ScanInterval:   20 * time.Millisecond,
		ProcessLister:  func(context.Context) ([]wrapper.ProcessInfo, error) { return agent.snapshot(), nil },
		IsProcessAlive: agent.isAlive,
		ProcessExitChecker: func(pid int) (int, bool) {
			agent.noteChecker(pid)
			if checker == nil {
				return 0, false
			}
			return checker(pid)
		},
		StartTimeResolver: agent.startTime,
		CwdResolver:       agent.cwd,
		PIDAllowlist:      []int{pid},
		IsDispatchPID:     func(int) bool { return false },
		AgyLogDir:         logDir,
		Quiet:             true,
		Agents: []config.AgentConfig{{
			ID:      "synth",
			Alias:   []string{"synth"},
			Enabled: boolPtr(true),
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		mgr.Stop()
		cancel()
	})
	mgr.Start(ctx)
	jobID := wrapper.GenerateAttachJobID("synth", pid, agent.start)
	return &synthRun{
		mgr:    mgr,
		client: client,
		agent:  agent,
		cancel: cancel,
		topic:  mqtt.JobStatusTopic("synth-box", jobID),
	}
}

func (s *synthAgent) snapshot() []wrapper.ProcessInfo {
	// Cwd and StartTime stay empty so attach must use the injected resolvers
	// instead of host ps, /proc, or Signal(0).
	return []wrapper.ProcessInfo{{
		PID:  s.pid,
		PPID: 1,
		Args: []string{"synth", "-p", synthTitle},
	}}
}

func waitSynthJob(t *testing.T, client *mockMQTTClient, topic string, ok func(model.Job) bool) model.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last model.Job
	var raw []byte
	for time.Now().Before(deadline) {
		if pub := getLastPublished(client, topic); pub != nil {
			raw = append([]byte(nil), pub.Payload...)
			if err := json.Unmarshal(pub.Payload, &last); err == nil && ok(last) {
				return last
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job on %s, last=%+v raw=%s", topic, last, raw)
	return last
}

func TestAttachSynthetic_RunningWhileAlive(t *testing.T) {
	run := startSynth(t, 910001, func(int) (int, bool) { return 0, false })
	job := waitSynthJob(t, run.client, run.topic, func(j model.Job) bool {
		return j.Status == model.StatusRunning
	})
	if job.Title != synthTitle || job.Cwd != synthCwd || !job.StartedAt.Equal(run.agent.start) {
		t.Fatalf("running job = %+v, want title %q cwd %q start %s", job, synthTitle, synthCwd, run.agent.start)
	}
	if run.mgr.ActiveCount() != 1 {
		t.Fatalf("active count = %d, want 1", run.mgr.ActiveCount())
	}
	before := len(run.client.GetPublished())
	deadline := time.Now().Add(2500 * time.Millisecond)
	refreshed := false
	for time.Now().Before(deadline) {
		if len(run.client.GetPublished()) > before {
			again := waitSynthJob(t, run.client, run.topic, func(j model.Job) bool { return true })
			if again.Status == model.StatusRunning {
				refreshed = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !refreshed {
		t.Fatal("synthetic process stayed alive but no later running status was published")
	}
	if !run.agent.stillAlive() {
		t.Fatal("liveness record flipped without the test doing so")
	}
	if run.agent.checkerCount() != 0 {
		t.Fatalf("exit checker ran while the process was still alive: %d", run.agent.checkerCount())
	}
	run.agent.callsOnlyOurPID(t)
}

func TestAttachSynthetic_ExitNonZeroFailed(t *testing.T) {
	run := startSynth(t, 910002, func(int) (int, bool) { return synthExitCode, true })
	waitSynthJob(t, run.client, run.topic, func(j model.Job) bool {
		return j.Status == model.StatusRunning
	})
	run.agent.setAlive(false)
	job := waitSynthJob(t, run.client, run.topic, func(j model.Job) bool {
		return j.Status == model.StatusFailed || j.Status == model.StatusDone
	})
	if job.Status != model.StatusFailed {
		t.Fatalf("status = %q, want failed", job.Status)
	}
	if job.Error != nonZeroExitErr {
		t.Fatalf("error = %q, want %q", job.Error, nonZeroExitErr)
	}
	if job.Progress == 100 {
		t.Fatalf("failed job progress = %v", job.Progress)
	}
	if run.agent.checkerCount() < 1 {
		t.Fatal("known non-zero exit was not read from the injected checker")
	}
	run.agent.callsOnlyOurPID(t)
}

func TestAttachSynthetic_ExitUnknownDoesNotInventCode(t *testing.T) {
	run := startSynth(t, 910003, func(int) (int, bool) {
		// 7 is deliberately not observable: ok=false must discard it.
		return synthExitCode, false
	})
	waitSynthJob(t, run.client, run.topic, func(j model.Job) bool {
		return j.Status == model.StatusRunning
	})
	run.agent.setAlive(false)
	job := waitSynthJob(t, run.client, run.topic, func(j model.Job) bool {
		return j.Status == model.StatusFailed || j.Status == model.StatusDone
	})
	if job.Status != model.StatusFailed {
		t.Fatalf("status = %q, want failed", job.Status)
	}
	if job.Error != unknownExitErr {
		t.Fatalf("error = %q, want %q", job.Error, unknownExitErr)
	}
	if strings.Contains(job.Error, "7") || strings.Contains(job.Error, "退出码 7") {
		t.Fatalf("unobservable checker value was reported as a code: %q", job.Error)
	}
	if run.agent.checkerCount() < 1 {
		t.Fatal("exit checker was not consulted")
	}
	run.agent.callsOnlyOurPID(t)
}

func TestAttachSynthetic_StopDoesNotKillOrInventExit(t *testing.T) {
	run := startSynth(t, 910004, func(int) (int, bool) { return synthExitCode, true })
	waitSynthJob(t, run.client, run.topic, func(j model.Job) bool {
		return j.Status == model.StatusRunning
	})
	checksBefore := run.agent.checkerCount()
	run.mgr.Stop()
	job := waitSynthJob(t, run.client, run.topic, func(j model.Job) bool {
		return j.Status == model.StatusFailed || j.Status == model.StatusDone
	})
	if job.Status != model.StatusFailed {
		t.Fatalf("status = %q, want failed", job.Status)
	}
	if job.Error != interruptErr {
		t.Fatalf("error = %q, want %q", job.Error, interruptErr)
	}
	if strings.Contains(job.Error, "退出码") {
		t.Fatalf("stopping the monitor invented an exit code: %q", job.Error)
	}
	if !run.agent.stillAlive() {
		t.Fatal("stopping the monitor cleared the synthetic liveness record")
	}
	if run.agent.checkerCount() != checksBefore {
		t.Fatalf("exit checker calls %d -> %d while the process stayed alive", checksBefore, run.agent.checkerCount())
	}
	if run.mgr.ActiveCount() != 0 {
		t.Fatalf("active count after stop = %d, want 0", run.mgr.ActiveCount())
	}
	run.agent.callsOnlyOurPID(t)
}

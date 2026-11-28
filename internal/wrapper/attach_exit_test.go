//go:build darwin || windows

package wrapper_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

// TestHelperExitWithCode is a real short-lived child for exit-observation
// tests: it stays alive long enough to be attached, then exits with the
// requested code.
func TestHelperExitWithCode(t *testing.T) {
	if os.Getenv("AGENTPULSE_WANT_HELPER_PROCESS") != "1" {
		return
	}
	time.Sleep(500 * time.Millisecond)
	if os.Getenv("AGENTPULSE_HELPER_EXIT_CODE") == "7" {
		os.Exit(7)
	}
	os.Exit(0)
}

// runBareExitScenario attaches to a real child process without injecting a
// ProcessExitChecker, so the final status must come from observing the exit.
func runBareExitScenario(t *testing.T, exitCode string, wantStatus model.JobStatus, wantErrSubstring string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperExitWithCode$")
	cmd.Env = append(os.Environ(), "AGENTPULSE_WANT_HELPER_PROCESS=1", "AGENTPULSE_HELPER_EXIT_CODE="+exitCode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper child: %v", err)
	}
	childPID := cmd.Process.Pid

	var aliveMu sync.Mutex
	alive := true
	go func() {
		_ = cmd.Wait()
		aliveMu.Lock()
		alive = false
		aliveMu.Unlock()
	}()
	isAlive := func(pid int) bool {
		aliveMu.Lock()
		defer aliveMu.Unlock()
		return alive && pid == childPID
	}

	mockClient := newMockMQTTClient()
	startTime := time.Now().Add(-time.Second).UTC()
	mockProcs := []wrapper.ProcessInfo{{
		PID:       childPID,
		PPID:      1,
		StartTime: startTime,
		Args:      []string{"agy", "-p", "exit observation scenario"},
		Cwd:       t.TempDir(),
	}}
	mockLister := func(ctx context.Context) ([]wrapper.ProcessInfo, error) {
		aliveMu.Lock()
		a := alive
		aliveMu.Unlock()
		if a {
			return mockProcs, nil
		}
		return nil, nil
	}

	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "exit-obs-node",
		MQTTClient:     mockClient,
		Agents:         []config.AgentConfig{{ID: "agy", Alias: []string{"agy"}, Enabled: boolPtr(true)}},
		ScanInterval:   30 * time.Millisecond,
		ProcessLister:  mockLister,
		IsProcessAlive: isAlive,
		AgyLogDir:      t.TempDir(),
		Quiet:          true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)

	jobID := wrapper.GenerateAttachJobID("agy", childPID, startTime)
	statusTopic := mqtt.JobStatusTopic("exit-obs-node", jobID)

	var finalJob model.Job
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pub := getLastPublished(mockClient, statusTopic); pub != nil {
			_ = json.Unmarshal(pub.Payload, &finalJob)
			if finalJob.Status == model.StatusDone || finalJob.Status == model.StatusFailed {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	mgr.Stop()

	if finalJob.Status != wantStatus {
		t.Fatalf("bare child exit=%s: status = %q (error %q), want %q", exitCode, finalJob.Status, finalJob.Error, wantStatus)
	}
	if wantErrSubstring != "" && !strings.Contains(finalJob.Error, wantErrSubstring) {
		t.Fatalf("bare child exit=%s: error %q must contain %q", exitCode, finalJob.Error, wantErrSubstring)
	}
	if wantStatus == model.StatusDone && finalJob.Progress != 100.0 {
		t.Fatalf("done job progress = %f, want 100", finalJob.Progress)
	}
}

// TestAttachManagerBareExitZeroObserved pins issue #62: a bare process that
// exits 0 must be DONE, not FAILED with "无法判定退出码".
func TestAttachManagerBareExitZeroObserved(t *testing.T) {
	runBareExitScenario(t, "0", model.StatusDone, "")
}

// TestAttachManagerBareExitSevenObserved pins issue #62: a non-zero exit must
// be FAILED with the observed exit code.
func TestAttachManagerBareExitSevenObserved(t *testing.T) {
	runBareExitScenario(t, "7", model.StatusFailed, "退出码 7")
}

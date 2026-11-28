//go:build windows

package wrapper_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

func TestHelperCIMLines(t *testing.T) {
	if os.Getenv("AGENTPULSE_WANT_HELPER_PROCESS") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, "\"Pid\",\"Ppid\",\"Created\",\"Cmd\"\n\"12345\",\"1\",\"2026-11-27 10:00:00\",\"\"\"C:\\bin\\agy.exe\"\" -p task\"\n")
	os.Exit(0)
}

// TestWindowsAllowlistDiscoveryViaCIM exercises the Windows routing, CSV
// parsing and .exe matching through the commandContext seam with fixture
// output. Real CIM/powershell behavior still requires a Windows machine run.
func TestWindowsAllowlistDiscoveryViaCIM(t *testing.T) {
	restore := wrapper.SetIsolationHooksForTest(wrapper.IsolationHooks{
		CommandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name != "powershell" {
				t.Errorf("expected powershell CIM query, got %q", name)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperCIMLines$")
			cmd.Env = append(os.Environ(), "AGENTPULSE_WANT_HELPER_PROCESS=1")
			return cmd
		},
	})
	t.Cleanup(restore)

	mockClient := newMockMQTTClient()
	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "win-cim-node",
		MQTTClient:     mockClient,
		PIDAllowlist:   []int{12345},
		ScanInterval:   30 * time.Millisecond,
		Agents:         []config.AgentConfig{{ID: "agy", Alias: []string{"agy"}, Enabled: boolPtr(true)}},
		IsProcessAlive: func(int) bool { return true },
		AgyLogDir:      t.TempDir(),
		Quiet:          true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for mgr.ActiveCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	active := mgr.ActiveCount()
	mgr.Stop()

	if active != 1 {
		t.Fatalf("expected 1 attached job from CIM fixture, got %d", active)
	}
}

func TestWindowsProcessAliveSelf(t *testing.T) {
	if !wrapper.IsProcessAlive(os.Getpid()) {
		t.Fatal("own process must be reported alive")
	}
	if wrapper.IsProcessAlive(99999999) {
		t.Fatal("nonexistent PID must not be reported alive")
	}
}

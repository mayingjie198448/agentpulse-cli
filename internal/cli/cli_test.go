package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
)

func TestRootCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"version", []string{"version"}},
		{"doctor", []string{"doctor"}},
		{"list", []string{"list"}},
		{"list_json", []string{"list", "--json"}},
		{"logs", []string{"logs", "job-agy-01", "--tail", "5"}},
		{"status", []string{"status"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := cli.NewRootCmd()
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs(tt.args)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error executing %s: %v", tt.name, err)
			}
		})
	}
}

func TestDoctorCommand_Output(t *testing.T) {
	// 1. Default mock mode
	t.Run("default mock mode", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "In-memory simulation store active (default)") {
			t.Errorf("expected default mock store message, got: %s", out)
		}
	})

	// 2. Explicit MQTT url mode
	t.Run("mqtt url configured", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor", "--mqtt-url", "tcp://127.0.0.1:18833"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "MQTT Store configured (Target: tcp://127.0.0.1:18833)") {
			t.Errorf("expected MQTT Store configured output, got: %s", out)
		}
		if !strings.Contains(out, "Checking connectivity") {
			t.Errorf("expected checking connectivity line, got: %s", out)
		}
	})
}

func TestCLI_StoreSelection(t *testing.T) {
	// 1. Invalid store flag
	t.Run("invalid store flag", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"list", "--store", "invalid-store"})
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for invalid store flag, got nil")
		}
		if !strings.Contains(err.Error(), "invalid store type") {
			t.Errorf("expected error message to mention invalid store type, got: %v", err)
		}
	})

	// 2. MQTT store enabled but no URL
	t.Run("mqtt without url", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"list", "--store", "mqtt"})
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for mqtt without url, got nil")
		}
		if !strings.Contains(err.Error(), "no broker URL provided") {
			t.Errorf("expected error message about missing broker URL, got: %v", err)
		}
	})

	// 3. Fallback: AGENTPULSE_MQTT_URL env overridden by --store=mock
	t.Run("env url overridden by store=mock", func(t *testing.T) {
		os.Setenv("AGENTPULSE_MQTT_URL", "tcp://127.0.0.1:18833")
		defer os.Unsetenv("AGENTPULSE_MQTT_URL")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"list", "--store", "mock"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("expected mock to run successfully despite env var: %v", err)
		}
	})

	// 4. Settle timeout flag in doctor output
	t.Run("mqtt settle timeout flag", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor", "--mqtt-url", "tcp://127.0.0.1:18833", "--mqtt-settle-timeout", "350ms"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "settle=350ms") {
			t.Errorf("expected doctor output to show settle=350ms, got: %s", out)
		}
	})

	// 5. Settle timeout env in doctor output
	t.Run("mqtt settle timeout env", func(t *testing.T) {
		os.Setenv("AGENTPULSE_MQTT_SETTLE_TIMEOUT", "450ms")
		defer os.Unsetenv("AGENTPULSE_MQTT_SETTLE_TIMEOUT")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor", "--mqtt-url", "tcp://127.0.0.1:18833"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "settle=450ms") {
			t.Errorf("expected doctor output to show settle=450ms, got: %s", out)
		}
	})
}

func TestWrapCommand(t *testing.T) {
	// 1. Missing command error
	t.Run("missing command error", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"wrap"})
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error when wrapping without command, got nil")
		}
		if !strings.Contains(err.Error(), "command to wrap is required") {
			t.Errorf("expected missing command error, got: %v", err)
		}
	})

	// 2. Stub mode execution
	t.Run("stub mode execution", func(t *testing.T) {
		tmpDir := t.TempDir()
		statusFile := filepath.Join(tmpDir, "stub-cli-status.json")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"wrap", "--stub", "--engine", "opencode", "--status-file", statusFile, "--title", "CLI Stub Test"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error executing stub wrap: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "Job completed successfully") {
			t.Errorf("expected success banner, got: %s", out)
		}

		// Verify status file was created
		data, err := os.ReadFile(statusFile)
		if err != nil {
			t.Fatalf("failed to read status file: %v", err)
		}
		if !strings.Contains(string(data), `"done"`) {
			t.Errorf("expected status 'done' in status file: %s", string(data))
		}
	})

	// 3. Process execution with echo
	t.Run("process execution", func(t *testing.T) {
		tmpDir := t.TempDir()
		statusFile := filepath.Join(tmpDir, "echo-cli-status.json")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"wrap", "--status-file", statusFile, "--quiet", "echo", "test output"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error executing wrap echo: %v", err)
		}

		// Verify status file
		data, err := os.ReadFile(statusFile)
		if err != nil {
			t.Fatalf("failed to read status file: %v", err)
		}
		if !strings.Contains(string(data), `"done"`) {
			t.Errorf("expected done status: %s", string(data))
		}
	})

	// 4. Missing binary failure (never silent stub)
	t.Run("missing binary failure", func(t *testing.T) {
		tmpDir := t.TempDir()
		statusFile := filepath.Join(tmpDir, "missing-bin-cli-status.json")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"wrap", "--status-file", statusFile, "--quiet", "nonexistent-cli-bin-99999"})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error from missing binary, got nil")
		}

		data, err := os.ReadFile(statusFile)
		if err != nil {
			t.Fatalf("failed to read status file: %v", err)
		}
		if !strings.Contains(string(data), `"failed"`) {
			t.Errorf("expected status 'failed' in status file: %s", string(data))
		}
	})

	// 5. Explicit stub with missing binary succeeds
	t.Run("explicit stub with missing binary succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		statusFile := filepath.Join(tmpDir, "explicit-stub-cli-status.json")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"wrap", "--stub", "--status-file", statusFile, "--quiet", "nonexistent-cli-bin-99999"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("expected explicit stub to succeed: %v", err)
		}

		data, err := os.ReadFile(statusFile)
		if err != nil {
			t.Fatalf("failed to read status file: %v", err)
		}
		if !strings.Contains(string(data), `"done"`) {
			t.Errorf("expected status 'done' in status file: %s", string(data))
		}
	})

	// 6. MQTT connect failure marks status failed (never stuck at running)
	t.Run("mqtt connect failure sets status failed", func(t *testing.T) {
		tmpDir := t.TempDir()
		statusFile := filepath.Join(tmpDir, "mqtt-fail-cli-status.json")

		os.Setenv("AGENTPULSE_MQTT_TIMEOUT", "100ms")
		defer os.Unsetenv("AGENTPULSE_MQTT_TIMEOUT")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{
			"wrap",
			"--status-file", statusFile,
			"--mqtt-url", "tcp://127.0.0.1:18833",
			"echo", "should not run",
		})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected wrap to fail on unreachable broker, got nil")
		}

		out := buf.String()
		if !strings.Contains(out, "Connecting to MQTT broker tcp://127.0.0.1:18833...") {
			t.Errorf("expected 'Connecting to MQTT broker' banner, got: %s", out)
		}
		if strings.Contains(out, "Connected to MQTT broker") {
			t.Errorf("expected no false 'Connected to MQTT broker' banner, got: %s", out)
		}
		if strings.Contains(out, "Executing:") {
			t.Errorf("expected no 'Executing:' banner when MQTT setup fails, got: %s", out)
		}

		data, err := os.ReadFile(statusFile)
		if err != nil {
			t.Fatalf("failed to read status file: %v", err)
		}
		if !strings.Contains(string(data), `"failed"`) {
			t.Errorf("expected status 'failed' in status file: %s", string(data))
		}
		if !strings.Contains(string(data), `"online": false`) && !strings.Contains(string(data), `"online":false`) {
			t.Errorf("expected online: false in status file: %s", string(data))
		}
	})
}

func TestStatusCommand(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "status.json")

	// 1. Missing status file reports clean message
	t.Run("missing status file", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"status", "--status-file", filepath.Join(tmpDir, "absent.json")})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "No local status snapshot found") {
			t.Errorf("expected not found message, got: %s", buf.String())
		}
	})

	// 2. Populate status file via wrap, then read via status
	t.Run("read status after wrap", func(t *testing.T) {
		wrapCmd := cli.NewRootCmd()
		wrapBuf := new(bytes.Buffer)
		wrapCmd.SetOut(wrapBuf)
		wrapCmd.SetErr(wrapBuf)
		wrapCmd.SetArgs([]string{
			"wrap",
			"--status-file", statusFile,
			"--machine-id", "test-box-01",
			"--title", "CLI Status Verification",
			"--quiet",
			"echo", "Status probe line",
		})
		if err := wrapCmd.Execute(); err != nil {
			t.Fatalf("wrap command failed: %v", err)
		}

		// Test table / human-readable output
		statusCmd := cli.NewRootCmd()
		statusBuf := new(bytes.Buffer)
		statusCmd.SetOut(statusBuf)
		statusCmd.SetErr(statusBuf)
		statusCmd.SetArgs([]string{"status", "--status-file", statusFile})

		if err := statusCmd.Execute(); err != nil {
			t.Fatalf("status command failed: %v", err)
		}

		out := statusBuf.String()
		if !strings.Contains(out, "test-box-01") {
			t.Errorf("expected machine ID in status output: %s", out)
		}
		if !strings.Contains(out, "done") {
			t.Errorf("expected status 'done' in status output: %s", out)
		}
		if !strings.Contains(out, "CLI Status Verification") {
			t.Errorf("expected title in status output: %s", out)
		}

		// Test JSON output
		jsonCmd := cli.NewRootCmd()
		jsonBuf := new(bytes.Buffer)
		jsonCmd.SetOut(jsonBuf)
		jsonCmd.SetErr(jsonBuf)
		jsonCmd.SetArgs([]string{"status", "--status-file", statusFile, "--json"})

		if err := jsonCmd.Execute(); err != nil {
			t.Fatalf("status --json failed: %v", err)
		}

		jsonOut := jsonBuf.String()
		if !strings.Contains(jsonOut, `"id": "test-box-01"`) {
			t.Errorf("expected machine ID in JSON: %s", jsonOut)
		}
		if !strings.Contains(jsonOut, `"status": "done"`) {
			t.Errorf("expected done status in JSON: %s", jsonOut)
		}
	})
}

func TestDispatchCommand(t *testing.T) {
	// 1. Missing machine
	t.Run("missing machine", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"dispatch", "--mqtt-url", "tcp://127.0.0.1:18833", "--", "agy", "-p", "test"})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for missing machine, got nil")
		}
		if !strings.Contains(err.Error(), "machine ID is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// 2. Missing command
	t.Run("missing command", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"dispatch", "--mqtt-url", "tcp://127.0.0.1:18833", "--machine", "box-1"})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for missing command, got nil")
		}
		if !strings.Contains(err.Error(), "command to dispatch is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// 3. Missing broker URL
	t.Run("missing broker url", func(t *testing.T) {
		// Clear env var if set
		orig := os.Getenv("AGENTPULSE_MQTT_URL")
		os.Unsetenv("AGENTPULSE_MQTT_URL")
		defer func() {
			if orig != "" {
				os.Setenv("AGENTPULSE_MQTT_URL", orig)
			}
		}()

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"dispatch", "--machine", "box-1", "--", "agy", "-p", "test"})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for missing broker url, got nil")
		}
		if !strings.Contains(err.Error(), "broker URL is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// 4. Connecting banner does not claim connected before connect succeeds
	t.Run("connecting banner behavior on unreachable broker", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{
			"dispatch",
			"--mqtt-url", "tcp://127.0.0.1:18833",
			"--machine", "box-1",
			"--", "agy", "-p", "test",
		})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected connection error, got nil")
		}

		out := buf.String()
		if !strings.Contains(out, "Connecting to MQTT broker tcp://127.0.0.1:18833...") {
			t.Errorf("expected connecting banner, got: %s", out)
		}
		if strings.Contains(out, "Connected to MQTT broker") {
			t.Errorf("expected no false connected banner, got: %s", out)
		}
	})

	// 5. Flags --timeout-seconds and --wait-timeout are accepted
	t.Run("timeout flags accepted", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{
			"dispatch",
			"--mqtt-url", "tcp://127.0.0.1:18833",
			"--machine", "box-1",
			"--timeout-seconds", "1800",
			"--wait-timeout", "60",
			"--", "agy", "-p", "test",
		})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected connection error, got nil")
		}
		if strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("unexpected flag error: %v", err)
		}
	})
}

func TestListenCommand(t *testing.T) {
	// 1. Missing broker URL
	t.Run("missing broker url", func(t *testing.T) {
		orig := os.Getenv("AGENTPULSE_MQTT_URL")
		os.Unsetenv("AGENTPULSE_MQTT_URL")
		defer func() {
			if orig != "" {
				os.Setenv("AGENTPULSE_MQTT_URL", orig)
			}
		}()

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"listen"})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for missing broker url, got nil")
		}
		if !strings.Contains(err.Error(), "broker URL is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	// 2. Connecting banner behavior on unreachable broker
	t.Run("connecting banner behavior on unreachable broker", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{
			"listen",
			"--mqtt-url", "tcp://127.0.0.1:18833",
			"--machine", "test-listener-node",
		})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for unreachable broker, got nil")
		}

		out := buf.String()
		if !strings.Contains(out, "Connecting to MQTT broker tcp://127.0.0.1:18833...") {
			t.Errorf("expected connecting banner, got: %s", out)
		}
		if strings.Contains(out, "Connected to MQTT broker") {
			t.Errorf("expected no false connected banner, got: %s", out)
		}
	})

	// 3. wrap --listen invokes listener
	t.Run("wrap --listen unreachable broker", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{
			"wrap",
			"--listen",
			"--mqtt-url", "tcp://127.0.0.1:18833",
			"--machine", "test-wrap-listen-node",
		})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error for unreachable broker, got nil")
		}

		out := buf.String()
		if !strings.Contains(out, "Connecting to MQTT broker tcp://127.0.0.1:18833...") {
			t.Errorf("expected connecting banner, got: %s", out)
		}
		if strings.Contains(out, "Connected to MQTT broker") {
			t.Errorf("expected no false connected banner, got: %s", out)
		}
	})
}

func TestListCommand_OnlineFilter(t *testing.T) {
	// 1. Default list shows all machines including offline ones (e.g. macbook in mock)
	t.Run("default list shows offline machines", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"list"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "macbook") {
			t.Errorf("expected macbook to be present in full list, got: %s", out)
		}
		if !strings.Contains(out, "OFFLINE") {
			t.Errorf("expected OFFLINE status in output, got: %s", out)
		}
		if !strings.Contains(out, "HEARTBEAT") {
			t.Errorf("expected HEARTBEAT column header in output, got: %s", out)
		}
	})

	// 2. list --online filters out offline machines
	t.Run("list --online excludes offline machines", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"list", "--online"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		parts := strings.Split(out, "JOBS:")
		machOut := parts[0]
		if strings.Contains(machOut, "macbook") {
			t.Errorf("expected macbook (offline) to be filtered out of machines list, got: %s", machOut)
		}
		if strings.Contains(machOut, "OFFLINE") {
			t.Errorf("expected no OFFLINE rows with --online, got: %s", machOut)
		}
		if !strings.Contains(machOut, "ccdq") || !strings.Contains(machOut, "mac-mini") || !strings.Contains(machOut, "home-pc") {
			t.Errorf("expected online machines to be present, got: %s", machOut)
		}
		if !strings.Contains(machOut, "HEARTBEAT") {
			t.Errorf("expected HEARTBEAT column to be preserved, got: %s", machOut)
		}
	})

	// 3. list --online --json outputs only online machines
	t.Run("list --online --json", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"list", "--online", "--json"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var payload struct {
			Machines []struct {
				ID     string `json:"id"`
				Online bool   `json:"online"`
			} `json:"machines"`
		}
		if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
			t.Fatalf("failed to unmarshal JSON output: %v", err)
		}

		if len(payload.Machines) != 3 {
			t.Fatalf("expected 3 online machines, got %d", len(payload.Machines))
		}
		for _, m := range payload.Machines {
			if !m.Online {
				t.Errorf("expected machine %s to be online", m.ID)
			}
			if m.ID == "macbook" {
				t.Errorf("macbook should not be present in online list")
			}
		}
	})
}

func TestDispatchCommand_ForceFlag(t *testing.T) {
	cmd := cli.NewRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"dispatch",
		"--mqtt-url", "tcp://127.0.0.1:18833",
		"--machine", "box-1",
		"--force",
		"--", "agy", "-p", "test",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected connection error to unreachable broker, got nil")
	}
	if strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("unexpected unknown flag error for --force: %v", err)
	}
}

func TestDispatchCommand_MachineSanitized(t *testing.T) {
	cmd := cli.NewRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"dispatch",
		"--mqtt-url", "tcp://127.0.0.1:18833",
		"--machine", "Mac_Studio",
		"--force",
		"--", "agy", "-p", "test",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected connection error to unreachable broker, got nil")
	}
	if strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("unexpected unknown flag error: %v", err)
	}
}

func TestResolveMQTTConfig_HeartbeatFlagsAndEnv(t *testing.T) {
	// 1. Default values via doctor with no flags
	t.Run("defaults", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"doctor"})
		_ = cmd.Execute()

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.HeartbeatTimeout != 45*time.Second {
			t.Errorf("expected default HeartbeatTimeout 45s, got %v", cfg.HeartbeatTimeout)
		}
		if cfg.HeartbeatCheckInterval != 10*time.Second {
			t.Errorf("expected default HeartbeatCheckInterval 10s, got %v", cfg.HeartbeatCheckInterval)
		}
	})

	// 2. Env variable configuration
	t.Run("env variables", func(t *testing.T) {
		os.Setenv("AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT", "60s")
		os.Setenv("AGENTPULSE_MQTT_HEARTBEAT_CHECK_INTERVAL", "15s")
		defer func() {
			os.Unsetenv("AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT")
			os.Unsetenv("AGENTPULSE_MQTT_HEARTBEAT_CHECK_INTERVAL")
		}()

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"doctor"})
		_ = cmd.Execute()

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.HeartbeatTimeout != 60*time.Second {
			t.Errorf("expected HeartbeatTimeout 60s from env, got %v", cfg.HeartbeatTimeout)
		}
		if cfg.HeartbeatCheckInterval != 15*time.Second {
			t.Errorf("expected HeartbeatCheckInterval 15s from env, got %v", cfg.HeartbeatCheckInterval)
		}
	})

	// 3. Flags override env
	t.Run("flags override env", func(t *testing.T) {
		os.Setenv("AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT", "60s")
		os.Setenv("AGENTPULSE_MQTT_HEARTBEAT_CHECK_INTERVAL", "15s")
		defer func() {
			os.Unsetenv("AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT")
			os.Unsetenv("AGENTPULSE_MQTT_HEARTBEAT_CHECK_INTERVAL")
		}()

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{
			"doctor",
			"--mqtt-heartbeat-timeout", "25s",
			"--mqtt-heartbeat-check-interval", "5s",
		})
		_ = cmd.Execute()

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.HeartbeatTimeout != 25*time.Second {
			t.Errorf("expected HeartbeatTimeout 25s from flag, got %v", cfg.HeartbeatTimeout)
		}
		if cfg.HeartbeatCheckInterval != 5*time.Second {
			t.Errorf("expected HeartbeatCheckInterval 5s from flag, got %v", cfg.HeartbeatCheckInterval)
		}
	})
}

func TestCLI_TLSConfiguration(t *testing.T) {
	// 1. Flags resolution
	t.Run("tls flags resolution", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{
			"doctor",
			"--mqtt-url", "tcp://broker.example:8883",
			"--mqtt-tls",
			"--mqtt-ca-cert", "/test/ca.pem",
			"--mqtt-client-cert", "/test/client.crt",
			"--mqtt-client-key", "/test/client.key",
			"--mqtt-tls-insecure",
			"--mqtt-server-name", "sni.example",
		})
		_ = cmd.Execute()

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if !cfg.TLSEnable {
			t.Errorf("expected TLSEnable=true from flag")
		}
		if cfg.CACertFile != "/test/ca.pem" {
			t.Errorf("expected CACertFile='/test/ca.pem', got %q", cfg.CACertFile)
		}
		if cfg.ClientCertFile != "/test/client.crt" {
			t.Errorf("expected ClientCertFile='/test/client.crt', got %q", cfg.ClientCertFile)
		}
		if cfg.ClientKeyFile != "/test/client.key" {
			t.Errorf("expected ClientKeyFile='/test/client.key', got %q", cfg.ClientKeyFile)
		}
		if !cfg.InsecureSkipVerify {
			t.Errorf("expected InsecureSkipVerify=true from flag")
		}
		if cfg.ServerName != "sni.example" {
			t.Errorf("expected ServerName='sni.example', got %q", cfg.ServerName)
		}
		if !cfg.IsTLSEnabled() {
			t.Errorf("expected IsTLSEnabled()=true")
		}
		if cfg.BrokerEndpoint() != "tls://broker.example:8883" {
			t.Errorf("expected BrokerEndpoint='tls://broker.example:8883', got %q", cfg.BrokerEndpoint())
		}
	})

	// 2. Env variables resolution
	t.Run("tls env variables resolution", func(t *testing.T) {
		os.Setenv("AGENTPULSE_MQTT_TLS", "true")
		os.Setenv("AGENTPULSE_MQTT_CA_CERT", "/env/ca.pem")
		os.Setenv("AGENTPULSE_MQTT_CLIENT_CERT", "/env/client.crt")
		os.Setenv("AGENTPULSE_MQTT_CLIENT_KEY", "/env/client.key")
		os.Setenv("AGENTPULSE_MQTT_TLS_INSECURE", "1")
		os.Setenv("AGENTPULSE_MQTT_SERVER_NAME", "env.sni")
		defer func() {
			os.Unsetenv("AGENTPULSE_MQTT_TLS")
			os.Unsetenv("AGENTPULSE_MQTT_CA_CERT")
			os.Unsetenv("AGENTPULSE_MQTT_CLIENT_CERT")
			os.Unsetenv("AGENTPULSE_MQTT_CLIENT_KEY")
			os.Unsetenv("AGENTPULSE_MQTT_TLS_INSECURE")
			os.Unsetenv("AGENTPULSE_MQTT_SERVER_NAME")
		}()

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"doctor"})
		_ = cmd.Execute()

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if !cfg.TLSEnable {
			t.Errorf("expected TLSEnable=true from env")
		}
		if cfg.CACertFile != "/env/ca.pem" {
			t.Errorf("expected CACertFile='/env/ca.pem', got %q", cfg.CACertFile)
		}
		if cfg.ClientCertFile != "/env/client.crt" {
			t.Errorf("expected ClientCertFile='/env/client.crt', got %q", cfg.ClientCertFile)
		}
		if cfg.ClientKeyFile != "/env/client.key" {
			t.Errorf("expected ClientKeyFile='/env/client.key', got %q", cfg.ClientKeyFile)
		}
		if !cfg.InsecureSkipVerify {
			t.Errorf("expected InsecureSkipVerify=true from env")
		}
		if cfg.ServerName != "env.sni" {
			t.Errorf("expected ServerName='env.sni', got %q", cfg.ServerName)
		}
	})

	// 3. Flags override env
	t.Run("tls flags override env", func(t *testing.T) {
		os.Setenv("AGENTPULSE_MQTT_CA_CERT", "/env/ca.pem")
		os.Setenv("AGENTPULSE_MQTT_SERVER_NAME", "env.sni")
		defer func() {
			os.Unsetenv("AGENTPULSE_MQTT_CA_CERT")
			os.Unsetenv("AGENTPULSE_MQTT_SERVER_NAME")
		}()

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{
			"doctor",
			"--mqtt-ca-cert", "/flag/ca.pem",
			"--mqtt-server-name", "flag.sni",
		})
		_ = cmd.Execute()

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.CACertFile != "/flag/ca.pem" {
			t.Errorf("expected CACertFile to be overridden by flag, got %q", cfg.CACertFile)
		}
		if cfg.ServerName != "flag.sni" {
			t.Errorf("expected ServerName to be overridden by flag, got %q", cfg.ServerName)
		}
	})

	// 4. Doctor output with TLS enabled and verify password is never printed
	t.Run("doctor output displays TLS info and hides password", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{
			"doctor",
			"--mqtt-url", "tls://127.0.0.1:88833",
			"--mqtt-username", "operator_alice",
			"--mqtt-password", "super_secret_never_leak_pwd",
			"--mqtt-ca-cert", "/certs/ca.pem",
			"--mqtt-client-cert", "/certs/client.crt",
			"--mqtt-client-key", "/certs/client.key",
			"--mqtt-tls-insecure",
			"--mqtt-server-name", "test.broker",
		})

		_ = cmd.Execute()
		out := buf.String()

		// Verify TLS info line
		if !strings.Contains(out, "[INFO] MQTT TLS: enabled, insecure=true, ca=/certs/ca.pem, mTLS=true, sni=test.broker") {
			t.Errorf("expected doctor output to contain complete TLS summary line, got:\n%s", out)
		}

		// Verify username is printed
		if !strings.Contains(out, `auth=username "operator_alice"`) {
			t.Errorf("expected username in doctor auth info, got:\n%s", out)
		}

		// Hard requirement: Password must NEVER be printed
		if strings.Contains(out, "super_secret_never_leak_pwd") {
			t.Errorf("CRITICAL SECURITY VIOLATION: password was printed in doctor output:\n%s", out)
		}
	})
}



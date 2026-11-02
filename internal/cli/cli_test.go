package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

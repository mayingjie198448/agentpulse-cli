package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/spf13/cobra"
)

func TestRootCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name      string
		args      []string
		wantError string
	}{
		{"version", []string{"version"}, ""},
		{"doctor", []string{"doctor"}, ""},
		{"status", []string{"status"}, ""},
		{"list", []string{"list"}, "未配置 MQTT Broker URL"},
		{"list_json", []string{"list", "--json"}, "未配置 MQTT Broker URL"},
		{"logs", []string{"logs", "job-agy-01", "--tail", "5"}, "未配置 MQTT Broker URL"},
		{"jobs", []string{"jobs"}, "未配置 MQTT Broker URL"},
		{"jobs_delete", []string{"jobs", "delete", "job-1"}, "未配置 MQTT Broker URL"},
		{"jobs_rm", []string{"jobs", "rm", "job-1"}, "未配置 MQTT Broker URL"},
		{"watch", []string{"watch"}, "未配置 MQTT Broker URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := cli.NewRootCmd()
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("unexpected error executing %s: %v", tt.name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
		})
	}
}

func TestDoctorCommand_Output(t *testing.T) {
	// 1. No broker: guide the user to config instead of a simulated store.
	t.Run("no broker guides to config", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor", "--config", filepath.Join(t.TempDir(), "missing.yaml")})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "未配置 MQTT Broker URL") || !strings.Contains(out, "agentpulse config") {
			t.Errorf("expected config guidance, got: %s", out)
		}
		if strings.Contains(out, "mock store") || strings.Contains(out, "内存模拟") {
			t.Errorf("doctor must not advertise a mock store, got: %s", out)
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
		if !strings.Contains(out, "tcp://127.0.0.1:18833") {
			t.Errorf("expected MQTT Store configured output, got: %s", out)
		}
		if !strings.Contains(out, "Broker 连通性测试中") && !strings.Contains(out, "Checking connectivity") {
			t.Errorf("expected checking connectivity line, got: %s", out)
		}
	})

	// 3. Missing machine_id in config
	t.Run("missing machine_id warns clearly", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		cfg := &config.Config{
			Version: 1,
			Broker: config.BrokerConfig{
				URL: "tcp://127.0.0.1:18833",
			},
		}
		_ = config.SaveConfig(cfgPath, cfg)

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor", "--config", cfgPath})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "未配置 machine_id") {
			t.Errorf("expected missing machine_id warning, got: %s", out)
		}
	})

	// 4. Disabled agent shows [-]
	t.Run("disabled agent shows [-]", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		disabled := false
		cfg := &config.Config{
			Version:   1,
			MachineID: "test-node",
			Broker: config.BrokerConfig{
				URL: "tcp://127.0.0.1:18833",
			},
			Agents: []config.AgentConfig{
				{
					ID:      "test-disabled",
					Alias:   []string{"td"},
					Enabled: &disabled,
				},
			},
		}
		_ = config.SaveConfig(cfgPath, cfg)

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor", "--config", cfgPath})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "[-] test-disabled") {
			t.Errorf("expected disabled agent marker '[-] test-disabled', got: %s", out)
		}
	})
}

func TestCLI_StoreSelection(t *testing.T) {
	// 1. --store is no longer a product switch.
	t.Run("store flag removed", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"list", "--store", "mock"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("expected unknown flag for --store, got %v", err)
		}
	})

	// 2. Legacy AGENTPULSE_* env must not open a store.
	t.Run("legacy env does not select a store", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_URL", "tcp://127.0.0.1:18833")
		t.Setenv("AGENTPULSE_STORE", "mock")
		missing := filepath.Join(t.TempDir(), "missing.yaml")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"list", "--config", missing})

		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "未配置 MQTT Broker URL") {
			t.Fatalf("expected missing broker error, got %v", err)
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

	// 5. Settle timeout env is ignored; doctor keeps the default.
	t.Run("mqtt settle timeout env ignored", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_SETTLE_TIMEOUT", "450ms")
		missing := filepath.Join(t.TempDir(), "missing.yaml")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"doctor", "--config", missing, "--mqtt-url", "tcp://127.0.0.1:18833"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "settle=200ms") {
			t.Errorf("expected doctor output to keep default settle=200ms, got: %s", out)
		}
		if strings.Contains(out, "settle=450ms") {
			t.Errorf("legacy settle timeout env must be ignored, got: %s", out)
		}
		if strings.Contains(out, "检测到旧的环境变量") {
			t.Errorf("startup must not warn about legacy AGENTPULSE_* env, got: %s", out)
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

func TestWrapIgnoresLegacyStatusAndMachineEnv(t *testing.T) {
	tmp := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	envStatus := filepath.Join(tmp, "from-env.json")
	t.Setenv("AGENTPULSE_STATUS_FILE", envStatus)
	t.Setenv("AGENTPULSE_MACHINE_ID", "Custom_Box_99")
	t.Setenv("AGENTPULSE_MACHINE_NAME", "Env Box")
	t.Setenv("AGENTPULSE_JOB_ID", "job-from-env")

	cmd := cli.NewRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"wrap", "--stub", "--quiet", "--engine", "opencode"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wrap stub: %v", err)
	}
	if _, err := os.Stat(envStatus); !os.IsNotExist(err) {
		t.Fatalf("AGENTPULSE_STATUS_FILE must not be written, stat err=%v", err)
	}

	defaultStatus := filepath.Join(tmp, ".agentpulse", "status.json")
	data, err := os.ReadFile(defaultStatus)
	if err != nil {
		t.Fatalf("expected default status file: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "custom-box-99") || strings.Contains(text, "job-from-env") || strings.Contains(text, "Env Box") {
		t.Errorf("legacy identity env leaked into status: %s", text)
	}

	statusCmd := cli.NewRootCmd()
	statusBuf := new(bytes.Buffer)
	statusCmd.SetOut(statusBuf)
	statusCmd.SetErr(statusBuf)
	statusCmd.SetArgs([]string{"status"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	statusOut := statusBuf.String()
	if strings.Contains(statusOut, envStatus) {
		t.Errorf("status must not read AGENTPULSE_STATUS_FILE, got: %s", statusOut)
	}
	if !strings.Contains(statusOut, "done") {
		t.Errorf("expected status to read the default snapshot, got: %s", statusOut)
	}
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

	// 3. Missing broker URL (legacy env must not satisfy it)
	t.Run("missing broker url", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_URL", "tcp://127.0.0.1:18833")
		missing := filepath.Join(t.TempDir(), "missing.yaml")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"dispatch", "--config", missing, "--machine", "box-1", "--", "agy", "-p", "test"})

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
	// 1. Missing broker URL (legacy env must not satisfy it)
	t.Run("missing broker url", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_URL", "tcp://127.0.0.1:18833")
		missing := filepath.Join(t.TempDir(), "missing.yaml")

		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"listen", "--config", missing})

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

func TestResolveMQTTConfig_HeartbeatFlags(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	// 1. Default values
	t.Run("defaults", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"version", "--config", missing})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.HeartbeatTimeout != 45*time.Second {
			t.Errorf("expected default HeartbeatTimeout 45s, got %v", cfg.HeartbeatTimeout)
		}
		if cfg.HeartbeatCheckInterval != 10*time.Second {
			t.Errorf("expected default HeartbeatCheckInterval 10s, got %v", cfg.HeartbeatCheckInterval)
		}
	})

	// 2. Legacy env is ignored
	t.Run("env ignored", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT", "60s")
		t.Setenv("AGENTPULSE_MQTT_HEARTBEAT_CHECK_INTERVAL", "15s")

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"version", "--config", missing})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.HeartbeatTimeout != 45*time.Second {
			t.Errorf("expected HeartbeatTimeout to stay 45s, got %v", cfg.HeartbeatTimeout)
		}
		if cfg.HeartbeatCheckInterval != 10*time.Second {
			t.Errorf("expected HeartbeatCheckInterval to stay 10s, got %v", cfg.HeartbeatCheckInterval)
		}
	})

	// 3. Explicit flags apply even when legacy env is set
	t.Run("flags", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_HEARTBEAT_TIMEOUT", "60s")
		t.Setenv("AGENTPULSE_MQTT_HEARTBEAT_CHECK_INTERVAL", "15s")

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{
			"version",
			"--config", missing,
			"--mqtt-heartbeat-timeout", "25s",
			"--mqtt-heartbeat-check-interval", "5s",
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.HeartbeatTimeout != 25*time.Second {
			t.Errorf("expected HeartbeatTimeout 25s from flag, got %v", cfg.HeartbeatTimeout)
		}
		if cfg.HeartbeatCheckInterval != 5*time.Second {
			t.Errorf("expected HeartbeatCheckInterval 5s from flag, got %v", cfg.HeartbeatCheckInterval)
		}
	})
}

func TestResolveMQTTConfig_FileThenFlagsIgnoreEnv(t *testing.T) {
	t.Setenv("AGENTPULSE_MQTT_URL", "tcp://from-env.example:1883")
	t.Setenv("AGENTPULSE_MQTT_USERNAME", "env-user")
	t.Setenv("AGENTPULSE_MQTT_PASSWORD", "env-secret")
	t.Setenv("AGENTPULSE_MQTT_CLIENT_ID", "env-client")
	t.Setenv("AGENTPULSE_MQTT_SETTLE_TIMEOUT", "450ms")
	t.Setenv("AGENTPULSE_MQTT_TIMEOUT", "100ms")
	t.Setenv("AGENTPULSE_MQTT_TLS", "false")
	t.Setenv("AGENTPULSE_MQTT_CA_CERT", "/env/ca.pem")
	t.Setenv("AGENTPULSE_MQTT_SERVER_NAME", "env.sni")

	confPath := filepath.Join(t.TempDir(), "config.yaml")
	body := []byte(`version: 1
machine_id: test-box
broker:
  url: tls://broker.example:8883
  username: file-user
  password: file-secret
  tls: true
  ca_cert: /file/ca.pem
  server_name: file.sni
`)
	if err := os.WriteFile(confPath, body, 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("config file wins over env", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"version", "--config", confPath})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.BrokerURL != "tls://broker.example:8883" {
			t.Errorf("broker url: got %q", cfg.BrokerURL)
		}
		if cfg.Username != "file-user" || cfg.Password != "file-secret" {
			t.Errorf("credentials: user=%q pass=%q", cfg.Username, cfg.Password)
		}
		if cfg.ClientID == "env-client" {
			t.Error("client id must not come from env")
		}
		if !cfg.TLSEnable || cfg.CACertFile != "/file/ca.pem" || cfg.ServerName != "file.sni" {
			t.Errorf("tls from file not applied: tls=%v ca=%q sni=%q", cfg.TLSEnable, cfg.CACertFile, cfg.ServerName)
		}
		if cfg.SettleTimeout != 200*time.Millisecond {
			t.Errorf("settle timeout: got %v", cfg.SettleTimeout)
		}
		if cfg.Timeout != 10*time.Second {
			t.Errorf("connect timeout: got %v", cfg.Timeout)
		}
	})

	t.Run("flags override config file", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{
			"version",
			"--config", confPath,
			"--mqtt-url", "tcp://127.0.0.1:1883",
			"--mqtt-username", "flag-user",
			"--mqtt-settle-timeout", "350ms",
		})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.BrokerURL != "tcp://127.0.0.1:1883" {
			t.Errorf("flag url: got %q", cfg.BrokerURL)
		}
		if cfg.Username != "flag-user" {
			t.Errorf("flag user: got %q", cfg.Username)
		}
		if cfg.Password != "file-secret" {
			t.Errorf("password should stay from file, got %q", cfg.Password)
		}
		if cfg.SettleTimeout != 350*time.Millisecond {
			t.Errorf("flag settle: got %v", cfg.SettleTimeout)
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

	// 2. Legacy TLS env is ignored
	t.Run("tls env ignored", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_TLS", "true")
		t.Setenv("AGENTPULSE_MQTT_CA_CERT", "/env/ca.pem")
		t.Setenv("AGENTPULSE_MQTT_CLIENT_CERT", "/env/client.crt")
		t.Setenv("AGENTPULSE_MQTT_CLIENT_KEY", "/env/client.key")
		t.Setenv("AGENTPULSE_MQTT_TLS_INSECURE", "1")
		t.Setenv("AGENTPULSE_MQTT_SERVER_NAME", "env.sni")
		missing := filepath.Join(t.TempDir(), "missing.yaml")

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{"version", "--config", missing})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.TLSEnable {
			t.Errorf("expected TLSEnable to stay false when only legacy env is set")
		}
		if cfg.CACertFile != "" || cfg.ClientCertFile != "" || cfg.ClientKeyFile != "" || cfg.ServerName != "" || cfg.InsecureSkipVerify {
			t.Errorf("legacy TLS env must be ignored, got %+v", cfg)
		}
	})

	// 3. Flags still apply when legacy env is set
	t.Run("tls flags override leftover env", func(t *testing.T) {
		t.Setenv("AGENTPULSE_MQTT_CA_CERT", "/env/ca.pem")
		t.Setenv("AGENTPULSE_MQTT_SERVER_NAME", "env.sni")

		cmd := cli.NewRootCmd()
		cmd.SetArgs([]string{
			"version",
			"--mqtt-ca-cert", "/flag/ca.pem",
			"--mqtt-server-name", "flag.sni",
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cfg := cli.ResolveMQTTConfigForTest(cmd)
		if cfg.CACertFile != "/flag/ca.pem" {
			t.Errorf("expected CACertFile from flag, got %q", cfg.CACertFile)
		}
		if cfg.ServerName != "flag.sni" {
			t.Errorf("expected ServerName from flag, got %q", cfg.ServerName)
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

func TestRootCommand_050ProductBoundaries(t *testing.T) {
	cmd := cli.NewRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error running --help: %v", err)
	}

	helpText := buf.String()

	// Visible commands must be present
	expectedVisible := []string{"config", "doctor", "dispatch", "jobs", "watch", "version"}
	for _, sub := range expectedVisible {
		if !strings.Contains(helpText, sub) {
			t.Errorf("expected %q to be visible in root help, got:\n%s", sub, helpText)
		}
	}

	// Chinese help text requirement
	if !strings.Contains(helpText, "AgentPulse 监控并调度多台机器") {
		t.Errorf("expected Chinese description in root help, got:\n%s", helpText)
	}

	// Hidden subcommands must NOT appear in visible Available Commands list
	for _, hidden := range []string{"listen", "wrap"} {
		// When hidden, cobra will not include them in the Available Commands list
		lines := strings.Split(helpText, "\n")
		inAvailableCommands := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Available Commands:") {
				inAvailableCommands = true
				continue
			}
			if inAvailableCommands && strings.HasPrefix(trimmed, "Flags:") {
				inAvailableCommands = false
				break
			}
			if inAvailableCommands && strings.HasPrefix(trimmed, hidden+" ") {
				t.Errorf("hidden command %q should NOT be listed in Available Commands, line: %s", hidden, line)
			}
		}
	}
}

func TestJobsCommand_RequiresBroker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"jobs"}, {"jobs", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := cli.NewRootCmd()
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs(args)

			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "未配置 MQTT Broker URL") {
				t.Fatalf("expected missing broker error, got %v", err)
			}
			if strings.Contains(buf.String(), "mock") {
				t.Errorf("jobs output must not mention mock, got: %s", buf.String())
			}
		})
	}
}

func TestRootCommand_NoConfigFails(t *testing.T) {
	// 旧环境变量不能代替配置文件，也不能打开模拟存储。
	t.Setenv("AGENTPULSE_MQTT_URL", "tcp://127.0.0.1:1883")
	t.Setenv("AGENTPULSE_STORE", "mock")

	tmpDir := t.TempDir()
	emptyConfig := filepath.Join(tmpDir, "nonexistent-config.yaml")

	t.Run("bare root command without config returns error", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"--config", emptyConfig})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error when running bare root command without config, got nil")
		}
		expectedErr := "未配置 MQTT Broker URL。请运行 'agentpulse config' 初始化配置，或通过 --mqtt-url 指定"
		if !strings.Contains(err.Error(), expectedErr) {
			t.Errorf("expected error %q, got %q", expectedErr, err.Error())
		}
	})

	t.Run("root command with --plain without config returns error", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"--config", emptyConfig, "--plain"})

		err := cmd.Execute()
		if err == nil {
			t.Fatalf("expected error when running root command with --plain without config, got nil")
		}
		expectedErr := "未配置 MQTT Broker URL。请运行 'agentpulse config' 初始化配置，或通过 --mqtt-url 指定"
		if !strings.Contains(err.Error(), expectedErr) {
			t.Errorf("expected error %q, got %q", expectedErr, err.Error())
		}
	})

	t.Run("store mock flag is rejected", func(t *testing.T) {
		cmd := cli.NewRootCmd()
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"--config", emptyConfig, "--store", "mock", "--plain"})

		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("expected --store=mock to be rejected, got %v", err)
		}
	})
}

func TestDispatchCommand_HelpAndSetInterspersed(t *testing.T) {
	cmd := cli.NewRootCmd()
	var dispatchCmd *cobra.Command
	for _, c := range cmd.Commands() {
		if c.Name() == "dispatch" {
			dispatchCmd = c
			break
		}
	}
	if dispatchCmd == nil {
		t.Fatalf("dispatch command not found")
	}

	usage := dispatchCmd.UsageString()

	// Verify Use string reflects flags before alias/command
	expectedUse := "dispatch [--machine <id>] [--force] <别名|CLI> -- <参数…>"
	if !strings.Contains(usage, expectedUse) {
		t.Errorf("expected usage to contain %q, got:\n%s", expectedUse, usage)
	}

	// Verify examples in Long description put flags before alias/command
	expectedExamples := []string{
		"agentpulse dispatch --machine mac-studio op -- -p \"重构认证模块\"",
		"agentpulse dispatch --machine mac-studio --follow agy -- -p \"运行测试集\"",
		"agentpulse dispatch --machine gpu-box --force agy -- -p \"预热模型\"",
	}
	for _, ex := range expectedExamples {
		if !strings.Contains(dispatchCmd.Long, ex) {
			t.Errorf("expected Long description to contain example %q, got:\n%s", ex, dispatchCmd.Long)
		}
	}

	// Verify flags parsed when placed before alias
	t.Run("flags before alias correctly parsed", func(t *testing.T) {
		rootCmd := cli.NewRootCmd()
		rootCmd.SetArgs([]string{
			"dispatch",
			"--mqtt-url", "tcp://127.0.0.1:18833",
			"--machine", "box-test-1",
			"--force",
			"agy",
			"--", "-p", "test",
		})
		err := rootCmd.Execute()
		if err == nil {
			t.Fatalf("expected broker connection error, got nil")
		}
		// Confirm it did not complain about missing machine or unknown flag
		if strings.Contains(err.Error(), "machine ID is required") {
			t.Errorf("machine flag was not parsed properly: %v", err)
		}
		if strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("unexpected unknown flag error: %v", err)
		}
	})

	// Verify SetInterspersed(false) behavior: flags after alias are not parsed as flags
	t.Run("flags after alias are treated as positional arguments", func(t *testing.T) {
		rootCmd := cli.NewRootCmd()
		rootCmd.SetArgs([]string{
			"dispatch",
			"--mqtt-url", "tcp://127.0.0.1:18833",
			"agy",
			"--machine", "box-test-1",
		})
		err := rootCmd.Execute()
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		// Since --machine comes after "agy", machineFlag is empty, so dispatch tries auto-select and fails with machine ID required
		if !strings.Contains(err.Error(), "machine ID is required") {
			t.Errorf("expected 'machine ID is required' when flag is misplaced after alias, got: %v", err)
		}
	})
}

func TestJobsDeleteCommand_StructureAndHelp(t *testing.T) {
	rootCmd := cli.NewRootCmd()

	// 1. Verify subcommand registration and alias
	jobsCmd, _, err := rootCmd.Find([]string{"jobs"})
	if err != nil {
		t.Fatalf("failed to find jobs command: %v", err)
	}

	deleteCmd, _, err := jobsCmd.Find([]string{"delete"})
	if err != nil {
		t.Fatalf("failed to find jobs delete command: %v", err)
	}
	if len(deleteCmd.Aliases) == 0 || deleteCmd.Aliases[0] != "rm" {
		t.Errorf("expected delete alias 'rm', got: %v", deleteCmd.Aliases)
	}

	// 2. Help text verification
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"jobs", "delete", "--help"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("failed to execute jobs delete --help: %v", err)
	}

	helpOut := buf.String()
	for _, expected := range []string{"delete <job-id>", "清除该任务的保留状态", "--force", "-y"} {
		if !strings.Contains(helpOut, expected) {
			t.Errorf("expected help output to contain %q, got:\n%s", expected, helpOut)
		}
	}
}

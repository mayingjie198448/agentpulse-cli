package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestRunConfigWizard_FreshInstall(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Guided sequence for new configuration:
	// 1. machine_id: test-machine-01
	// 2. Broker URL: tcp://127.0.0.1:1883
	// 3. Username: testuser
	// 4. Password: testpass
	// 5. TLS: n
	// 6. Agents customize: n (keeps probed defaults)
	inputLines := []string{
		"test-machine-01",
		"tcp://127.0.0.1:1883",
		"testuser",
		"testpass",
		"n",
		"n",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizard(strings.NewReader(input), &out, confPath)
	if err != nil {
		t.Fatalf("RunConfigWizard failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "[步骤 1/3]") ||
		!strings.Contains(outStr, "[步骤 2/3]") ||
		!strings.Contains(outStr, "[步骤 3/3]") {
		t.Errorf("expected 3 guided steps in output, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "Doctor") {
		t.Errorf("expected doctor verification in output, got:\n%s", outStr)
	}

	// Verify permissions on Unix
	if runtime.GOOS != "windows" {
		info, err := os.Stat(confPath)
		if err != nil {
			t.Fatalf("failed to stat config file: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("expected mode 0600, got %#o", info.Mode().Perm())
		}
	}

	// Verify saved config
	cfg, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}
	if cfg.MachineID != "test-machine-01" {
		t.Errorf("expected MachineID 'test-machine-01', got %q", cfg.MachineID)
	}
	if cfg.Broker.URL != "tcp://127.0.0.1:1883" {
		t.Errorf("expected broker URL 'tcp://127.0.0.1:1883', got %q", cfg.Broker.URL)
	}
	if cfg.Broker.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", cfg.Broker.Username)
	}
	if len(cfg.Agents) == 0 {
		t.Errorf("expected default agents to be saved")
	}
}

func TestRunConfigWizard_CustomAgentsAndEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Guided sequence:
	// 1. machine_id: custom-box
	// 2. Broker URL: tcp://127.0.0.1:1883
	// 3. Username: (empty)
	// 4. Password: (empty)
	// 5. TLS: n
	// 6. Agents customize: y
	// 7. agy: enabled y, path /custom/bin/agy, aliases agy,ai
	// 8. opencode: enabled n (disabled)
	// 9. codex: enabled y, path (empty), aliases codex
	// 10. add more agents: n
	inputLines := []string{
		"custom-box",
		"tcp://127.0.0.1:1883",
		"",
		"",
		"n",
		"y",
		"y",
		"/custom/bin/agy",
		"agy,ai",
		"n",
		"y",
		"",
		"codex",
		"n",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizard(strings.NewReader(input), &out, confPath)
	if err != nil {
		t.Fatalf("RunConfigWizard failed: %v", err)
	}

	cfg, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.MachineID != "custom-box" {
		t.Errorf("expected machine_id 'custom-box', got %q", cfg.MachineID)
	}

	// Verify agy
	var agyFound, opencodeFound bool
	for _, ag := range cfg.Agents {
		if ag.ID == "agy" {
			agyFound = true
			if !ag.IsEnabled() {
				t.Errorf("expected agy to be enabled")
			}
			if ag.Path != "/custom/bin/agy" {
				t.Errorf("expected agy path '/custom/bin/agy', got %q", ag.Path)
			}
			if len(ag.Alias) != 2 || ag.Alias[1] != "ai" {
				t.Errorf("expected agy aliases [agy, ai], got %v", ag.Alias)
			}
		}
		if ag.ID == "opencode" {
			opencodeFound = true
			if ag.IsEnabled() {
				t.Errorf("expected opencode to be disabled")
			}
		}
	}
	if !agyFound || !opencodeFound {
		t.Errorf("expected to find both agy and opencode in saved config")
	}
}

func TestRunConfigWizard_ExistingConfig_SectionedEditBroker(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Seed existing config
	initialCfg := &config.Config{
		Version:   1,
		MachineID: "persisted-box",
		Rev:       2,
		Broker: config.BrokerConfig{
			URL:      "tls://broker.example:8883",
			Username: "olduser",
			TLS:      true,
		},
		Agents: config.DefaultAgents(),
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("failed to seed config: %v", err)
	}

	// Simulated user input:
	// Choice: 2 (Broker only)
	// Broker URL: tcp://127.0.0.1:1883
	// Username: newuser
	// Password: (empty)
	// TLS: n
	inputLines := []string{
		"2",
		"tcp://127.0.0.1:1883",
		"newuser",
		"",
		"n",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizard(strings.NewReader(input), &out, confPath)
	if err != nil {
		t.Fatalf("RunConfigWizard failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "检测到已存在配置文件") {
		t.Errorf("expected existing config detection in output, got:\n%s", outStr)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load updated config: %v", err)
	}

	// MachineID should be untouched
	if loaded.MachineID != "persisted-box" {
		t.Errorf("expected MachineID 'persisted-box' to remain unchanged, got %q", loaded.MachineID)
	}
	// Broker should be updated
	if loaded.Broker.URL != "tcp://127.0.0.1:1883" {
		t.Errorf("expected updated broker URL 'tcp://127.0.0.1:1883', got %q", loaded.Broker.URL)
	}
	if loaded.Broker.Username != "newuser" {
		t.Errorf("expected updated username 'newuser', got %q", loaded.Broker.Username)
	}
	if len(loaded.Agents) != len(config.DefaultAgents()) {
		t.Errorf("expected agents to remain intact")
	}
}

func TestRunConfigWizard_ExistingConfig_EnterKeepsDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Seed existing config
	initialCfg := &config.Config{
		Version:   1,
		MachineID: "keep-box",
		Rev:       5,
		Broker: config.BrokerConfig{
			URL:      "tls://broker.example:8883",
			Username: "keepuser",
			Password: "keeppassword",
			TLS:      true,
		},
		Agents: config.DefaultAgents(),
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("failed to seed config: %v", err)
	}

	// Simulated user input:
	// Mode: 1 (All)
	// machine_id: Enter (keeps keep-box)
	// broker URL: Enter (keeps tls://broker.example:8883)
	// username: Enter (keeps keepuser)
	// password: Enter (keeps keeppassword)
	// TLS: Enter (keeps true)
	// agents customize: Enter (keeps false)
	inputLines := []string{
		"1",
		"",
		"",
		"",
		"",
		"",
		"",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizard(strings.NewReader(input), &out, confPath)
	if err != nil {
		t.Fatalf("RunConfigWizard failed: %v", err)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load updated config: %v", err)
	}

	if loaded.MachineID != "keep-box" {
		t.Errorf("expected MachineID 'keep-box', got %q", loaded.MachineID)
	}
	if loaded.Broker.URL != "tls://broker.example:8883" {
		t.Errorf("expected Broker URL 'tls://broker.example:8883', got %q", loaded.Broker.URL)
	}
	if loaded.Broker.Username != "keepuser" {
		t.Errorf("expected Username 'keepuser', got %q", loaded.Broker.Username)
	}
	if loaded.Broker.Password != "keeppassword" {
		t.Errorf("expected Password 'keeppassword', got %q", loaded.Broker.Password)
	}
	if !loaded.Broker.TLS {
		t.Errorf("expected TLS true")
	}
}

package cli_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestRunConfigWizard(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Simulated user input:
	// 1. broker URL: tcp://127.0.0.1:1883
	// 2. username: testuser
	// 3. password: testpass
	// 4. TLS: n
	// 5. connection test fail: retry? n
	// 6. machine_id: test-machine-01
	// 7. adopt probed agents? y
	inputLines := []string{
		"tcp://127.0.0.1:1883",
		"testuser",
		"testpass",
		"n",
		"n",
		"test-machine-01",
		"y",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizard(strings.NewReader(input), &out, confPath)
	if err != nil {
		t.Fatalf("RunConfigWizard failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "[步骤 1/4]") ||
		!strings.Contains(outStr, "[步骤 2/4]") ||
		!strings.Contains(outStr, "[步骤 3/4]") ||
		!strings.Contains(outStr, "[步骤 4/4]") {
		t.Errorf("expected all 4 steps in output, got:\n%s", outStr)
	}

	// Verify config file was saved correctly
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
		t.Errorf("expected agents to be saved")
	}
}

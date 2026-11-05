package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestDefaultConfigPath(t *testing.T) {
	p := config.DefaultConfigPath()
	if p == "" {
		t.Fatal("expected non-empty default config path")
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(p, "agentpulse") || !strings.HasSuffix(p, "config.yaml") {
			t.Errorf("unexpected windows config path: %s", p)
		}
	} else {
		if !strings.HasSuffix(p, filepath.Join(".config", "agentpulse", "config.yaml")) {
			t.Errorf("unexpected unix config path: %s", p)
		}
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "sub", "config.yaml")

	cfg := &config.Config{
		Version:   1,
		MachineID: "test-box",
		Rev:       2,
		Broker: config.BrokerConfig{
			URL:      "tls://broker.example:8883",
			Username: "agentpulse",
			Password: "mock-password",
			TLS:      true,
		},
		Agents: []config.AgentConfig{
			{
				ID:    "agy",
				Path:  "",
				Alias: []string{"agy"},
			},
			{
				ID:    "opencode",
				Path:  "",
				Alias: []string{"op", "opencode"},
			},
		},
	}

	err := config.SaveConfig(confPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Verify permissions on Unix
	if runtime.GOOS != "windows" {
		info, err := os.Stat(confPath)
		if err != nil {
			t.Fatalf("failed to stat config file: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("expected file mode 0600, got %#o", info.Mode().Perm())
		}

		dirInfo, err := os.Stat(filepath.Dir(confPath))
		if err != nil {
			t.Fatalf("failed to stat config dir: %v", err)
		}
		if dirInfo.Mode().Perm() != 0700 {
			t.Errorf("expected dir mode 0700, got %#o", dirInfo.Mode().Perm())
		}
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if loaded.MachineID != "test-box" {
		t.Errorf("expected MachineID 'test-box', got %q", loaded.MachineID)
	}
	if loaded.Broker.URL != "tls://broker.example:8883" {
		t.Errorf("expected broker URL 'tls://broker.example:8883', got %q", loaded.Broker.URL)
	}
	if len(loaded.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(loaded.Agents))
	}
	if loaded.Agents[1].ID != "opencode" {
		t.Errorf("expected agent 1 ID 'opencode', got %q", loaded.Agents[1].ID)
	}
}

func TestProbeAgent(t *testing.T) {
	// Probe a command that exists on all unix/mac/windows systems, e.g., "go" or "sh"
	cmdName := "go"
	st := config.ProbeAgent(config.AgentConfig{
		ID:    cmdName,
		Alias: []string{cmdName},
	})
	if !st.Ok {
		t.Errorf("expected %s to be found and ok: true", cmdName)
	}
	if st.ResolvedPath == "" {
		t.Errorf("expected non-empty resolved path for %s", cmdName)
	}

	// Probe non-existent binary
	nonExistent := config.ProbeAgent(config.AgentConfig{
		ID:    "nonexistent-binary-xyz-12345",
		Alias: []string{"nonexistent-alias"},
	})
	if nonExistent.Ok {
		t.Errorf("expected nonexistent binary to be ok: false")
	}

	// Probe disabled agent
	disabled := false
	disabledAgent := config.ProbeAgent(config.AgentConfig{
		ID:      cmdName,
		Alias:   []string{cmdName},
		Enabled: &disabled,
	})
	if disabledAgent.Ok {
		t.Errorf("expected disabled agent to be ok: false")
	}
	if !strings.Contains(disabledAgent.Detail, "禁用") {
		t.Errorf("expected detail to mention 禁用, got %s", disabledAgent.Detail)
	}
}

func TestFilterChildProcessEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"AGENTPULSE_MQTT_PASSWORD=supersecret",
		"BROKER_PASSWORD=hidden",
		"USER=alice",
	}
	filtered := config.FilterChildProcessEnv(env)
	for _, item := range filtered {
		if strings.Contains(item, "supersecret") || strings.Contains(item, "hidden") {
			t.Errorf("sensitive variable was not filtered: %s", item)
		}
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 filtered items, got %d: %v", len(filtered), filtered)
	}
}

func TestResolveAgentBinary(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:    "custom-agent",
				Path:  "/bin/echo",
				Alias: []string{"ca", "custom"},
			},
			{
				ID:    "fallback-agent",
				Path:  "",
				Alias: []string{"echo-alias"},
			},
		},
	}

	// 1. Matched by ID with explicit path
	res := config.ResolveAgentBinary(cfg, "custom-agent")
	if !strings.Contains(res, "echo") {
		t.Errorf("expected /bin/echo, got %q", res)
	}

	// 2. Matched by alias
	res = config.ResolveAgentBinary(cfg, "ca")
	if !strings.Contains(res, "echo") {
		t.Errorf("expected /bin/echo via alias 'ca', got %q", res)
	}

	// 3. Matched by alias with empty path, finding in PATH
	res = config.ResolveAgentBinary(cfg, "echo-alias")
	if res == "" {
		t.Errorf("expected non-empty resolution for echo-alias, got %q", res)
	}

	// 4. Fallback to direct PATH lookup
	res = config.ResolveAgentBinary(cfg, "ls")
	if !strings.Contains(res, "ls") {
		t.Errorf("expected 'ls' to resolve to PATH, got %q", res)
	}
}

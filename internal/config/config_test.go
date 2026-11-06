package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
		"SSH_CLIENT=100.1.2.3 54321 22",
		"SSH_CONNECTION=100.1.2.3 54321 100.1.2.4 22",
		"SSH_TTY=/dev/pts/1",
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
	}
	filtered := config.FilterChildProcessEnv(env)
	for _, item := range filtered {
		if strings.Contains(item, "supersecret") || strings.Contains(item, "hidden") {
			t.Errorf("sensitive variable was not filtered: %s", item)
		}
		if strings.HasPrefix(item, "SSH_CLIENT=") || strings.HasPrefix(item, "SSH_CONNECTION=") || strings.HasPrefix(item, "SSH_TTY=") {
			t.Errorf("SSH session variable was not filtered: %s", item)
		}
	}
	// Expected kept: PATH, USER, SSH_AUTH_SOCK (3 items)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 filtered items, got %d: %v", len(filtered), filtered)
	}
}

func TestMaxConcurrentJobsConfig(t *testing.T) {
	// Test default 1 when omitted
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := "version: 1\nmachine_id: test-mach\n"
	if err := os.WriteFile(confPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.MaxConcurrentJobs != 1 {
		t.Errorf("expected default MaxConcurrentJobs=1, got %d", cfg.MaxConcurrentJobs)
	}

	// Test custom concurrency
	cfg.MaxConcurrentJobs = 3
	if err := config.SaveConfig(confPath, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("LoadConfig after save failed: %v", err)
	}
	if loaded.MaxConcurrentJobs != 3 {
		t.Errorf("expected MaxConcurrentJobs=3, got %d", loaded.MaxConcurrentJobs)
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

func TestHasAgyModelEvidence(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "contains gemini model",
			output:   "gemini-3.8-flash-high  Gemini 3.8 Flash (High)",
			expected: true,
		},
		{
			name:     "contains claude model",
			output:   "claude-sonnet-4-6      Claude Sonnet 4.6 (Thinking)",
			expected: true,
		},
		{
			name: "spinner with models and storage notice",
			output: "Using file-based token storage because SSH session detected\n" +
				"⠋ Fetching available models...\n" +
				"gemini-3.8-flash-high  Gemini 3.8 Flash (High)\n" +
				"gpt-oss-120b-medium    GPT-OSS 120B (Medium)\n",
			expected: true,
		},
		{
			name:     "arbitrary two column model entry",
			output:   "deepseek-v3   DeepSeek V3",
			expected: true,
		},
		{
			name:     "empty string",
			output:   "",
			expected: false,
		},
		{
			name:     "only storage notice",
			output:   "Using file-based token storage because SSH session detected",
			expected: false,
		},
		{
			name:     "only spinner line",
			output:   "⠋ Fetching available models...",
			expected: false,
		},
		{
			name:     "error line",
			output:   "Error: failed to connect",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.HasAgyModelEvidence(tt.output)
			if got != tt.expected {
				t.Errorf("HasAgyModelEvidence(%q) = %v, want %v", tt.output, got, tt.expected)
			}
		})
	}
}

func TestProbeAgyAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script mock test on windows")
	}

	createMockScript := func(t *testing.T, content string) string {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "mock-agy")
		script := fmt.Sprintf("#!/bin/sh\n%s\n", content)
		if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
			t.Fatalf("failed to write mock script: %v", err)
		}
		return scriptPath
	}

	t.Run("explicit success with model list reports ready", func(t *testing.T) {
		bin := createMockScript(t, `echo "gemini-3.8-flash-high Gemini 3.8 Flash (High)"; exit 0`)
		ok, detail := config.ProbeAgyAuth(bin)
		if !ok {
			t.Errorf("expected ok=true, got ok=false, detail=%q", detail)
		}
		if detail != "已登录就绪" {
			t.Errorf("expected detail '已登录就绪', got %q", detail)
		}
	})

	t.Run("unauthenticated error output reports not logged in", func(t *testing.T) {
		bin := createMockScript(t, `echo "Please sign in to view available models"; exit 1`)
		ok, detail := config.ProbeAgyAuth(bin)
		if ok {
			t.Errorf("expected ok=false for unauthenticated, got ok=true")
		}
		if !strings.Contains(detail, "未登录") {
			t.Errorf("expected detail mentioning 未登录, got %q", detail)
		}
	})

	t.Run("token source error output reports not logged in", func(t *testing.T) {
		bin := createMockScript(t, `echo "error getting token source: You are not logged into Antigravity."; exit 1`)
		ok, detail := config.ProbeAgyAuth(bin)
		if ok {
			t.Errorf("expected ok=false for token source error, got ok=true")
		}
		if !strings.Contains(detail, "未登录") {
			t.Errorf("expected detail mentioning 未登录, got %q", detail)
		}
	})

	t.Run("timeout reports probe timeout rather than ready", func(t *testing.T) {
		bin := createMockScript(t, `sleep 1; exit 0`)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		ok, detail := config.ProbeAgyAuthContext(ctx, bin)
		if ok {
			t.Errorf("expected ok=false for timeout, got ok=true (fake ready bug)")
		}
		if !strings.Contains(detail, "超时") {
			t.Errorf("expected detail mentioning 超时, got %q", detail)
		}
	})

	t.Run("non-zero exit failure reports probe failure rather than ready", func(t *testing.T) {
		bin := createMockScript(t, `echo "connection reset by peer"; exit 2`)
		ok, detail := config.ProbeAgyAuth(bin)
		if ok {
			t.Errorf("expected ok=false for command failure, got ok=true (fake ready bug)")
		}
		if !strings.Contains(detail, "探测失败") {
			t.Errorf("expected detail mentioning 探测失败, got %q", detail)
		}
	})

	t.Run("storage notice alone without models reports unknown output rather than ready", func(t *testing.T) {
		bin := createMockScript(t, `echo "Using file-based token storage because SSH session detected"; exit 0`)
		ok, detail := config.ProbeAgyAuth(bin)
		if ok {
			t.Errorf("expected ok=false for storage notice alone, got ok=true")
		}
		if !strings.Contains(detail, "未知输出") {
			t.Errorf("expected detail mentioning 未知输出, got %q", detail)
		}
	})

	t.Run("empty output reports unknown output rather than ready", func(t *testing.T) {
		bin := createMockScript(t, `exit 0`)
		ok, detail := config.ProbeAgyAuth(bin)
		if ok {
			t.Errorf("expected ok=false for empty output, got ok=true")
		}
		if !strings.Contains(detail, "未知输出") {
			t.Errorf("expected detail mentioning 未知输出, got %q", detail)
		}
	})
}

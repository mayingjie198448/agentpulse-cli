package cli_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/autostart"
	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestRunConfigWizard_FreshInstall(t *testing.T) {
	tmpDir := t.TempDir()
	setSyntheticProfile(t, tmpDir)
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Guided sequence for new configuration:
	// 1. machine_id: test-machine-01
	// 2. Broker URL: tcp://127.0.0.1:1883
	// 3. Username: testuser
	// 4. Password: testpass
	// 5. TLS: n
	// 6. Agents auto-scan: y
	// 7. Agents rescan: n
	// 8. Select builtin engines: 1,2 (agy, opencode)
	// 9. Confirm agy: enabled y, path (empty), alias (empty -> default agy)
	// 10. Confirm opencode: enabled y, path (empty), alias (empty -> default op, opencode)
	// 11. Add custom CLI: n
	inputLines := []string{
		"test-machine-01",
		"tcp://127.0.0.1:1883",
		"testuser",
		"testpass",
		"n",
		"y",
		"n",
		"1,2",
		"y",
		"",
		"",
		"y",
		"",
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
	if !strings.Contains(outStr, "[步骤 1/3]") ||
		!strings.Contains(outStr, "[步骤 2/3]") ||
		!strings.Contains(outStr, "[步骤 3/3]") {
		t.Errorf("expected 3 guided steps in output, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "[步骤 1/4]") ||
		!strings.Contains(outStr, "[步骤 2/4]") ||
		!strings.Contains(outStr, "[步骤 3/4]") ||
		!strings.Contains(outStr, "[步骤 4/4]") {
		t.Errorf("expected 4 agent wizard sub-steps in output, got:\n%s", outStr)
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
	if len(cfg.Agents) != 2 {
		t.Fatalf("expected 2 agents saved, got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].ID != "agy" || cfg.Agents[1].ID != "opencode" {
		t.Errorf("expected [agy, opencode], got %+v", cfg.Agents)
	}
}

func TestRunConfigWizard_CustomAgentsAndEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	setSyntheticProfile(t, tmpDir)
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Guided sequence:
	// 1. machine_id: custom-box
	// 2. Broker URL: tcp://127.0.0.1:1883
	// 3. Username: (empty)
	// 4. Password: (empty)
	// 5. TLS: n
	// 6. Agents auto-scan: n (skip scan)
	// 7. Select builtin engines: 1,4 (agy and codex; opencode skipped)
	// 8. Confirm agy: enabled y, path /custom/bin/agy, aliases agy,ai
	// 9. Confirm codex: enabled y, path (empty), aliases codex
	// 10. Add custom CLI: y
	// 11. Custom ID: agy (rejected as built-in!)
	// 12. Custom ID: myagent
	// 13. Custom path: /usr/local/bin/myagent
	// 14. Custom aliases: myagent,bot
	// 15. Custom enabled: y
	// 16. Continue adding custom: n
	inputLines := []string{
		"custom-box",
		"tcp://127.0.0.1:1883",
		"",
		"",
		"n",
		"n",
		"1,4",
		"y",
		"/custom/bin/agy",
		"agy,ai",
		"y",
		"",
		"codex",
		"y",
		"agy",
		"y",
		"myagent",
		"/usr/local/bin/myagent",
		"myagent,bot",
		"y",
		"n",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizard(strings.NewReader(input), &out, confPath)
	if err != nil {
		t.Fatalf("RunConfigWizard failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "为系统预置引擎，不可作为自定义 CLI 追加") {
		t.Errorf("expected rejection of builtin agent name as custom, output:\n%s", outStr)
	}

	cfg, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.MachineID != "custom-box" {
		t.Errorf("expected machine_id 'custom-box', got %q", cfg.MachineID)
	}

	// Verify agy, codex, myagent
	var agyFound, codexFound, myagentFound, opencodeFound bool
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
			if ag.IsCustom() {
				t.Errorf("expected agy to NOT be custom")
			}
		}
		if ag.ID == "codex" {
			codexFound = true
			if !ag.IsEnabled() {
				t.Errorf("expected codex to be enabled")
			}
			if ag.IsCustom() {
				t.Errorf("expected codex to NOT be custom")
			}
		}
		if ag.ID == "myagent" {
			myagentFound = true
			if !ag.IsEnabled() {
				t.Errorf("expected myagent to be enabled")
			}
			if ag.Path != "/usr/local/bin/myagent" {
				t.Errorf("expected myagent path '/usr/local/bin/myagent', got %q", ag.Path)
			}
			if !ag.IsCustom() {
				t.Errorf("expected myagent to be marked as custom")
			}
		}
		if ag.ID == "opencode" {
			opencodeFound = true
		}
	}
	if !agyFound || !codexFound || !myagentFound {
		t.Errorf("expected to find agy, codex, and myagent in saved config, got: %+v", cfg.Agents)
	}
	if opencodeFound {
		t.Errorf("expected opencode to not be present since it wasn't selected")
	}

	// Verify builtin vs custom segregation
	builtins := cfg.BuiltinAgents()
	customs := cfg.CustomAgents()
	if len(builtins) != 2 {
		t.Errorf("expected 2 builtin agents, got %d", len(builtins))
	}
	if len(customs) != 1 || customs[0].ID != "myagent" {
		t.Errorf("expected 1 custom agent 'myagent', got %v", customs)
	}
}

func TestRunConfigWizard_ExistingConfig_SectionedEditBroker(t *testing.T) {
	tmpDir := t.TempDir()
	setSyntheticProfile(t, tmpDir)
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
	setSyntheticProfile(t, tmpDir)
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
	// agents: Enter (keeps existing agents)
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
	if len(loaded.Agents) != len(config.DefaultAgents()) {
		t.Errorf("expected %d agents, got %d", len(config.DefaultAgents()), len(loaded.Agents))
	}
}

func TestRunConfigWizard_RescanAndSectionAgents(t *testing.T) {
	tmpDir := t.TempDir()
	setSyntheticProfile(t, tmpDir)
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	// Seed existing config with 1 agent
	seedCfg := &config.Config{
		Version:   1,
		MachineID: "rescan-node",
		Broker: config.BrokerConfig{
			URL: "tcp://127.0.0.1:1883",
		},
		Agents: []config.AgentConfig{
			{ID: "agy", Alias: []string{"agy"}},
		},
	}
	if err := config.SaveConfig(confPath, seedCfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Directly invoke Section "agents"
	// Sequence:
	// 1. auto-scan: y
	// 2. rescan: y
	// 3. rescan: n (proceed)
	// 4. select builtin: 3 (claude)
	// 5. confirm claude: enabled y, path /opt/bin/claude, alias claude,c
	// 6. add custom: n
	inputLines := []string{
		"y",
		"y",
		"n",
		"3",
		"y",
		"/opt/bin/claude",
		"claude,c",
		"n",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizardWithSection(strings.NewReader(input), &out, confPath, "agents")
	if err != nil {
		t.Fatalf("RunConfigWizardWithSection failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "正在扫描本机 PATH 及常见安装路径...") {
		t.Errorf("expected scan execution in output, got:\n%s", outStr)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Untouched 'agy' must be preserved, and 'claude' added.
	if len(loaded.Agents) != 2 {
		t.Fatalf("expected 2 agents [agy, claude], got %d: %+v", len(loaded.Agents), loaded.Agents)
	}
	if loaded.Agents[0].ID != "agy" {
		t.Errorf("expected first agent to be preserved 'agy', got %q", loaded.Agents[0].ID)
	}
	if loaded.Agents[1].ID != "claude" {
		t.Errorf("expected second agent to be 'claude', got %q", loaded.Agents[1].ID)
	}
	if loaded.Agents[1].Path != "/opt/bin/claude" {
		t.Errorf("expected path '/opt/bin/claude', got %q", loaded.Agents[1].Path)
	}
}

func TestRunConfigWizard_PreserveExistingAgentsAndNoDuplicates(t *testing.T) {
	tmpDir := t.TempDir()
	setSyntheticProfile(t, tmpDir)
	confPath := filepath.Join(tmpDir, "agentpulse", "config.yaml")

	enabledTrue := true
	enabledFalse := false

	// Seed existing config with 1 custom agent and 2 built-in agents (one enabled, one disabled)
	initialCfg := &config.Config{
		Version:   1,
		MachineID: "preserve-node",
		Broker: config.BrokerConfig{
			URL: "tcp://127.0.0.1:1883",
		},
		Agents: []config.AgentConfig{
			{
				ID:      "agy",
				Path:    "/custom/bin/agy",
				Alias:   []string{"agy", "ai"},
				Enabled: &enabledTrue,
				Custom:  false,
			},
			{
				ID:      "custom-runner",
				Path:    "/opt/tools/custom-runner",
				Alias:   []string{"cr", "runner"},
				Enabled: &enabledTrue,
				Custom:  true,
			},
			{
				ID:      "codex",
				Path:    "/usr/bin/codex",
				Alias:   []string{"codex"},
				Enabled: &enabledFalse,
				Custom:  false,
			},
		},
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Scenario 1: Re-run wizard agents section, select only claude (3), attempt duplicate custom-runner & agy
	// Inputs:
	// 1. auto-scan: y
	// 2. rescan: n
	// 3. select builtin: 3 (claude only)
	// 4. confirm claude: enabled y, path /opt/bin/claude, alias claude,c
	// 5. add custom: y
	// 6. custom ID: custom-runner (duplicate of existing custom -> rejected!)
	// 7. add custom: y
	// 8. custom ID: agy (duplicate of builtin -> rejected!)
	// 9. add custom: n (exit custom loop)
	inputLines := []string{
		"y",
		"n",
		"3",
		"y",
		"/opt/bin/claude",
		"claude,c",
		"y",
		"custom-runner",
		"y",
		"agy",
		"n",
	}
	input := strings.Join(inputLines, "\n") + "\n"

	var out bytes.Buffer
	err := cli.RunConfigWizardWithSection(strings.NewReader(input), &out, confPath, "agents")
	if err != nil {
		t.Fatalf("RunConfigWizardWithSection failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "Agent ID \"custom-runner\" 已在列表中，请勿重复添加") {
		t.Errorf("expected duplicate custom-runner warning in output:\n%s", outStr)
	}
	if !strings.Contains(outStr, "为系统预置引擎，不可作为自定义 CLI 追加") {
		t.Errorf("expected builtin agy rejection as custom in output:\n%s", outStr)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Should contain 4 agents: agy, custom-runner, codex (untouched), and claude (added)
	if len(loaded.Agents) != 4 {
		t.Fatalf("expected 4 agents, got %d: %+v", len(loaded.Agents), loaded.Agents)
	}

	// Verify untouched agy
	agy := loaded.Agents[0]
	if agy.ID != "agy" || agy.Path != "/custom/bin/agy" || len(agy.Alias) != 2 || agy.Alias[1] != "ai" || !agy.IsEnabled() || agy.IsCustom() {
		t.Errorf("untouched agy properties were corrupted: %+v", agy)
	}

	// Verify untouched custom-runner
	cr := loaded.Agents[1]
	if cr.ID != "custom-runner" || cr.Path != "/opt/tools/custom-runner" || len(cr.Alias) != 2 || cr.Alias[0] != "cr" || !cr.IsEnabled() || !cr.IsCustom() {
		t.Errorf("untouched custom-runner properties were corrupted: %+v", cr)
	}

	// Verify untouched codex (disabled)
	codex := loaded.Agents[2]
	if codex.ID != "codex" || codex.Path != "/usr/bin/codex" || codex.IsEnabled() || codex.IsCustom() {
		t.Errorf("untouched codex properties were corrupted: %+v", codex)
	}

	// Verify newly added claude
	claude := loaded.Agents[3]
	if claude.ID != "claude" || claude.Path != "/opt/bin/claude" || len(claude.Alias) != 2 || !claude.IsEnabled() || claude.IsCustom() {
		t.Errorf("added claude properties mismatch: %+v", claude)
	}

	// Verify no duplicate IDs
	seenIDs := make(map[string]bool)
	for _, ag := range loaded.Agents {
		idLower := strings.ToLower(ag.ID)
		if seenIDs[idLower] {
			t.Errorf("duplicate agent ID found: %s", ag.ID)
		}
		seenIDs[idLower] = true
	}

	// Scenario 2: Re-run wizard on the updated config, selecting agy (1) and hitting Enter for defaults
	// User confirms agy without typing new path -> existing path /custom/bin/agy must NOT be overwritten!
	// Inputs:
	// 1. auto-scan: n
	// 2. select builtin: 1 (agy)
	// 3. confirm agy: enabled (Enter -> true), path (Enter -> keep /custom/bin/agy), alias (Enter -> keep agy, ai)
	// 4. add custom: n
	inputLines2 := []string{
		"n",
		"1",
		"",
		"",
		"",
		"n",
	}
	input2 := strings.Join(inputLines2, "\n") + "\n"

	var out2 bytes.Buffer
	err2 := cli.RunConfigWizardWithSection(strings.NewReader(input2), &out2, confPath, "agents")
	if err2 != nil {
		t.Fatalf("RunConfigWizardWithSection run 2 failed: %v", err2)
	}

	loaded2, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("LoadConfig run 2 failed: %v", err)
	}

	if len(loaded2.Agents) != 4 {
		t.Fatalf("expected 4 agents after run 2, got %d: %+v", len(loaded2.Agents), loaded2.Agents)
	}
	if loaded2.Agents[0].Path != "/custom/bin/agy" {
		t.Errorf("expected agy path to remain '/custom/bin/agy', got %q", loaded2.Agents[0].Path)
	}

	// No duplicate IDs after second run
	seenIDs2 := make(map[string]bool)
	for _, ag := range loaded2.Agents {
		idLower := strings.ToLower(ag.ID)
		if seenIDs2[idLower] {
			t.Errorf("duplicate agent ID found in run 2: %s", ag.ID)
		}
		seenIDs2[idLower] = true
	}
}

func TestRunConfigWizard_AutostartSection(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.yaml")

	initialCfg := &config.Config{
		Version:   1,
		MachineID: "box-autostart",
		Broker: config.BrokerConfig{
			URL: "tcp://127.0.0.1:1883",
		},
		Agents:    config.DefaultAgents(),
		Autostart: false,
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}

	cleanup := autostart.SetTestOverrides(t.TempDir(), t.TempDir(), func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	})
	defer cleanup()

	// 1. Edit section autostart -> enable with "y"
	var out bytes.Buffer
	if err := cli.RunConfigWizardWithSection(strings.NewReader("y\n"), &out, confPath, "autostart"); err != nil {
		t.Fatalf("wizard autostart failed: %v", err)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if runtime.GOOS == "darwin" && !loaded.Autostart {
		t.Errorf("expected Autostart=true on macOS after enabling")
	}

	// 2. Edit section autostart -> disable with "n"
	out.Reset()
	if err := cli.RunConfigWizardWithSection(strings.NewReader("n\n"), &out, confPath, "autostart"); err != nil {
		t.Fatalf("wizard autostart failed: %v", err)
	}
	loaded, err = config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if loaded.Autostart {
		t.Errorf("expected Autostart=false after disabling")
	}
}

func TestRunConfigWizard_MachineIDChange_AutoRestart(t *testing.T) {
	cleanup := autostart.SetTestOverrides(t.TempDir(), t.TempDir(), func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	})
	defer cleanup()

	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.yaml")

	initialCfg := &config.Config{
		Version:   1,
		MachineID: "old-box-id",
		Broker: config.BrokerConfig{
			URL: "tcp://127.0.0.1:1883",
		},
		Agents:    config.DefaultAgents(),
		Autostart: true,
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}

	var out bytes.Buffer
	// Edit machine_id section: enter "new-box-id"
	if err := cli.RunConfigWizardWithSection(strings.NewReader("new-box-id\n"), &out, confPath, "machine_id"); err != nil {
		t.Fatalf("wizard machine_id failed: %v", err)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if loaded.MachineID != "new-box-id" {
		t.Errorf("expected MachineID 'new-box-id', got %q", loaded.MachineID)
	}

	outStr := out.String()
	if runtime.GOOS == "darwin" {
		if !strings.Contains(outStr, "检测到 machine_id 变更") || !strings.Contains(outStr, "old-box-id") {
			t.Errorf("expected notification about machine_id change from old-box-id, got:\n%s", outStr)
		}
	}
}

func TestRunConfigWizard_BrokerAccountChange_AutoRestart(t *testing.T) {
	cleanup := autostart.SetTestOverrides(t.TempDir(), t.TempDir(), func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	})
	defer cleanup()

	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.yaml")

	initialCfg := &config.Config{
		Version:   1,
		MachineID: "box-test",
		Broker: config.BrokerConfig{
			URL:      "tcp://127.0.0.1:1883",
			Username: "old-user",
			Password: "old-password",
		},
		Agents:    config.DefaultAgents(),
		Autostart: true,
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}

	var out bytes.Buffer
	// Broker section prompts: URL, Username, Password, TLS
	// Enter URL default (\n), new username (new-user\n), new password (new-pass\n), TLS default (\n)
	input := "\nnew-user\nnew-pass\n\n"
	if err := cli.RunConfigWizardWithSection(strings.NewReader(input), &out, confPath, "broker"); err != nil {
		t.Fatalf("wizard broker failed: %v", err)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if loaded.Broker.Username != "new-user" || loaded.Broker.Password != "new-pass" {
		t.Errorf("expected updated credentials, got user=%q pass=%q", loaded.Broker.Username, loaded.Broker.Password)
	}

	outStr := out.String()
	if runtime.GOOS == "darwin" {
		if !strings.Contains(outStr, "检测到 Broker 配置变更") {
			t.Errorf("expected notification about broker change restart, got:\n%s", outStr)
		}
		if !strings.Contains(outStr, "已重启常驻守护进程并应用新 Broker 配置") {
			t.Errorf("expected notification about successful restart, got:\n%s", outStr)
		}
	}
}

func TestRunConfigWizard_BrokerTLSChange_AutoRestart(t *testing.T) {
	cleanup := autostart.SetTestOverrides(t.TempDir(), t.TempDir(), func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	})
	defer cleanup()

	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.yaml")

	initialCfg := &config.Config{
		Version:   1,
		MachineID: "box-test",
		Broker: config.BrokerConfig{
			URL: "tcp://127.0.0.1:1883",
			TLS: false,
		},
		Agents:    config.DefaultAgents(),
		Autostart: true,
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}

	var out bytes.Buffer
	// Broker section prompts: URL (\n), Username (\n), Password (\n), TLS (y\n)
	input := "\n\n\ny\n"
	if err := cli.RunConfigWizardWithSection(strings.NewReader(input), &out, confPath, "broker"); err != nil {
		t.Fatalf("wizard broker failed: %v", err)
	}

	loaded, err := config.LoadConfig(confPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if !loaded.Broker.TLS {
		t.Errorf("expected TLS=true after wizard update")
	}

	outStr := out.String()
	if runtime.GOOS == "darwin" {
		if !strings.Contains(outStr, "检测到 Broker 配置变更") {
			t.Errorf("expected notification about broker change restart for TLS, got:\n%s", outStr)
		}
	}
}

func TestRunConfigWizard_BrokerUnchanged_NoRestart(t *testing.T) {
	cleanup := autostart.SetTestOverrides(t.TempDir(), t.TempDir(), func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	})
	defer cleanup()

	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.yaml")

	initialCfg := &config.Config{
		Version:   1,
		MachineID: "box-test",
		Broker: config.BrokerConfig{
			URL:      "tcp://127.0.0.1:1883",
			Username: "user1",
			Password: "pass1",
			TLS:      false,
		},
		Agents:    config.DefaultAgents(),
		Autostart: true,
	}
	if err := config.SaveConfig(confPath, initialCfg); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}

	var out bytes.Buffer
	// Edit machine_id with same ID
	if err := cli.RunConfigWizardWithSection(strings.NewReader("box-test\n"), &out, confPath, "machine_id"); err != nil {
		t.Fatalf("wizard machine_id failed: %v", err)
	}

	outStr := out.String()
	if strings.Contains(outStr, "检测到 Broker 配置变更") {
		t.Errorf("expected NO broker change notification when broker is unchanged, got:\n%s", outStr)
	}
}

// setSyntheticProfile points HOME and the Windows profile variables at dir.
// UserHomeDir on Windows reads USERPROFILE, so HOME alone is not enough.
func setSyntheticProfile(t *testing.T, dir string) {
	t.Helper()
	roaming := filepath.Join(dir, "AppData", "Roaming")
	local := filepath.Join(dir, "AppData", "Local")
	if err := os.MkdirAll(roaming, 0o755); err != nil {
		t.Fatalf("mkdir AppData: %v", err)
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir LocalAppData: %v", err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("APPDATA", roaming)
	t.Setenv("LOCALAPPDATA", local)
}

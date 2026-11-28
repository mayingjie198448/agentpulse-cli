package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestBuiltinEngineDefs(t *testing.T) {
	defs := config.BuiltinEngineDefs()
	if len(defs) < 8 {
		t.Fatalf("expected at least 8 built-in engine definitions, got %d", len(defs))
	}

	expectedIDs := []string{
		"agy",
		"opencode",
		"claude",
		"codex",
		"gemini",
		"copilot",
		"aider",
		"goose",
		"cline",
	}

	for _, id := range expectedIDs {
		def, ok := config.LookupBuiltinEngine(id)
		if !ok {
			t.Errorf("expected engine %q to be in builtin definitions", id)
		}
		if def.ID != id {
			t.Errorf("expected def.ID %q, got %q", id, def.ID)
		}
		if !config.IsBuiltinEngine(id) {
			t.Errorf("expected IsBuiltinEngine(%q) to be true", id)
		}
	}

	// Case insensitivity
	if !config.IsBuiltinEngine("AGY") || !config.IsBuiltinEngine("Claude") {
		t.Error("expected IsBuiltinEngine to be case-insensitive")
	}

	// Unknown engine
	if config.IsBuiltinEngine("unknown-engine-xyz") {
		t.Error("expected IsBuiltinEngine('unknown-engine-xyz') to be false")
	}
}

func TestAgentConfig_CustomAndBuiltin(t *testing.T) {
	builtinAg := config.AgentConfig{
		ID:    "agy",
		Alias: []string{"agy"},
	}
	if !builtinAg.IsBuiltin() || builtinAg.IsCustom() {
		t.Errorf("expected agy to be builtin, got isBuiltin=%v, isCustom=%v", builtinAg.IsBuiltin(), builtinAg.IsCustom())
	}

	customAg := config.AgentConfig{
		ID:    "mybot",
		Alias: []string{"mybot"},
	}
	if customAg.IsBuiltin() || !customAg.IsCustom() {
		t.Errorf("expected mybot to be custom, got isBuiltin=%v, isCustom=%v", customAg.IsBuiltin(), customAg.IsCustom())
	}

	// Explicit custom flag
	explicitCustom := config.AgentConfig{
		ID:     "custom-runner",
		Custom: true,
	}
	if !explicitCustom.IsCustom() {
		t.Errorf("expected explicitCustom to be custom")
	}

	// Config separation
	cfg := &config.Config{
		Agents: []config.AgentConfig{builtinAg, customAg, explicitCustom},
	}
	builtins := cfg.BuiltinAgents()
	customs := cfg.CustomAgents()

	if len(builtins) != 1 || builtins[0].ID != "agy" {
		t.Errorf("expected 1 builtin agent 'agy', got %v", builtins)
	}
	if len(customs) != 2 {
		t.Errorf("expected 2 custom agents, got %d", len(customs))
	}
}

func createMockScanExec(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create bin dir %s: %v", dir, err)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".bat"
	}
	path := filepath.Join(dir, name+ext)
	content := "#!/bin/sh\necho mock\n"
	if runtime.GOOS == "windows" {
		content = "@echo off\r\necho mock\r\n"
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write mock binary %s: %v", path, err)
	}
	_ = os.Chmod(path, 0755)
	return path
}

func TestScanLocalAgents_Hermetic(t *testing.T) {
	fakeHome := t.TempDir()
	fakeBinDir := filepath.Join(fakeHome, "fakebin")

	// 1. Create mock executables for built-in engines: agy and claude
	agyPath := createMockScanExec(t, fakeBinDir, "agy")
	claudePath := createMockScanExec(t, fakeBinDir, "claude")

	// 2. Create mock custom CLI: cursor
	cursorPath := createMockScanExec(t, fakeBinDir, "cursor")

	// 3. Create mock IDE plugin: .continue directory in fake home
	continueDir := filepath.Join(fakeHome, ".continue")
	if err := os.MkdirAll(continueDir, 0755); err != nil {
		t.Fatalf("failed to create fake .continue dir: %v", err)
	}

	// Run hermetic scan
	scanRes := config.ScanLocalAgents(config.ScanOptions{
		OverridePATH: fakeBinDir,
		HomeDir:      fakeHome,
	})

	// Verify discovered built-in engines
	var foundAgy, foundClaude bool
	for _, b := range scanRes.DiscoveredBuiltin {
		if b.ID == "agy" {
			foundAgy = true
			if b.Path != agyPath {
				t.Errorf("expected agy path %q, got %q", agyPath, b.Path)
			}
		}
		if b.ID == "claude" {
			foundClaude = true
			if b.Path != claudePath {
				t.Errorf("expected claude path %q, got %q", claudePath, b.Path)
			}
		}
	}
	if !foundAgy {
		t.Errorf("expected agy to be discovered in builtin list")
	}
	if !foundClaude {
		t.Errorf("expected claude to be discovered in builtin list")
	}

	// Verify missing built-in engines
	var missingOpencode bool
	for _, m := range scanRes.MissingBuiltin {
		if m.ID == "opencode" {
			missingOpencode = true
		}
	}
	if !missingOpencode {
		t.Errorf("expected opencode to be in missing builtin list")
	}

	// Verify discovered custom CLI
	var foundCursor bool
	for _, c := range scanRes.DiscoveredCustom {
		if c.ID == "cursor" {
			foundCursor = true
			if c.Path != cursorPath {
				t.Errorf("expected cursor path %q, got %q", cursorPath, c.Path)
			}
			if c.Builtin {
				t.Errorf("expected cursor to NOT be marked as builtin")
			}
		}
	}
	if !foundCursor {
		t.Errorf("expected cursor to be discovered in custom list")
	}

	// Verify IDE plugin detection (Continue)
	var foundContinue bool
	for _, p := range scanRes.IDEPlugins {
		if p.Name == "Continue" {
			foundContinue = true
		}
	}
	if !foundContinue {
		t.Errorf("expected Continue to be detected as IDE plugin")
	}
}

func TestScanLocalAgents_OverridePATHExcludesHostAndSystemDirs(t *testing.T) {
	hostPATHDir := filepath.Join(t.TempDir(), "host-path")
	createMockScanExec(t, hostPATHDir, "codex")
	createMockScanExec(t, hostPATHDir, "cursor")
	t.Setenv("PATH", hostPATHDir)

	hostHome := filepath.Join(t.TempDir(), "host-home")
	createMockScanExec(t, filepath.Join(hostHome, ".local", "bin"), "opencode")
	if err := os.MkdirAll(filepath.Join(hostHome, ".continue"), 0755); err != nil {
		t.Fatalf("failed to create host .continue: %v", err)
	}
	t.Setenv("HOME", hostHome)
	t.Setenv("USERPROFILE", hostHome)

	overrideDir := filepath.Join(t.TempDir(), "override-bin")
	geminiPath := createMockScanExec(t, overrideDir, "gemini")

	// OverridePATH alone must not fall back to host PATH, system dirs, or host HomeDir.
	res := config.ScanLocalAgents(config.ScanOptions{
		OverridePATH: overrideDir,
	})

	if len(res.DiscoveredBuiltin) != 1 || res.DiscoveredBuiltin[0].ID != "gemini" || res.DiscoveredBuiltin[0].Path != geminiPath {
		t.Fatalf("expected only gemini at %q from OverridePATH, got %+v", geminiPath, res.DiscoveredBuiltin)
	}
	if len(res.DiscoveredCustom) != 0 {
		t.Fatalf("expected no custom CLIs from host PATH or system dirs, got %+v", res.DiscoveredCustom)
	}
	if len(res.IDEPlugins) != 0 {
		t.Fatalf("expected no IDE plugins from host HomeDir, got %+v", res.IDEPlugins)
	}

	// Verify OverridePATH + HomeDir stays strictly scoped to those two directories.
	fakeHome := filepath.Join(t.TempDir(), "fake-home")
	if err := os.MkdirAll(fakeHome, 0755); err != nil {
		t.Fatalf("failed to create fakeHome: %v", err)
	}
	resWithFakeHome := config.ScanLocalAgents(config.ScanOptions{
		OverridePATH: overrideDir,
		HomeDir:      fakeHome,
	})
	for _, b := range resWithFakeHome.DiscoveredBuiltin {
		if !strings.HasPrefix(b.Path, overrideDir) && !strings.HasPrefix(b.Path, fakeHome) {
			t.Errorf("discovered builtin %q leaked outside OverridePATH/HomeDir: %q", b.ID, b.Path)
		}
	}
	for _, c := range resWithFakeHome.DiscoveredCustom {
		if !strings.HasPrefix(c.Path, overrideDir) && !strings.HasPrefix(c.Path, fakeHome) {
			t.Errorf("discovered custom %q leaked outside OverridePATH/HomeDir: %q", c.ID, c.Path)
		}
	}
}

func TestScanLocalAgents_HomeDirOverrideDoesNotTouchHostHome(t *testing.T) {
	hostHome := filepath.Join(t.TempDir(), "real-host-home")
	createMockScanExec(t, filepath.Join(hostHome, ".local", "bin"), "opencode")
	createMockScanExec(t, filepath.Join(hostHome, ".antigravity", "bin"), "agy")
	if err := os.MkdirAll(filepath.Join(hostHome, ".continue"), 0755); err != nil {
		t.Fatalf("failed to create host .continue: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(hostHome, ".vscode", "extensions", "continue.continue-1.0.0"), 0755); err != nil {
		t.Fatalf("failed to create host vscode extension: %v", err)
	}
	t.Setenv("HOME", hostHome)
	t.Setenv("USERPROFILE", hostHome)

	overrideHome := filepath.Join(t.TempDir(), "override-home")
	claudePath := createMockScanExec(t, filepath.Join(overrideHome, ".local", "bin"), "claude")
	overrideBin := filepath.Join(t.TempDir(), "empty-bin")
	if err := os.MkdirAll(overrideBin, 0755); err != nil {
		t.Fatalf("failed to create overrideBin: %v", err)
	}

	res := config.ScanLocalAgents(config.ScanOptions{
		OverridePATH: overrideBin,
		HomeDir:      overrideHome,
	})

	if len(res.DiscoveredBuiltin) != 1 || res.DiscoveredBuiltin[0].ID != "claude" || res.DiscoveredBuiltin[0].Path != claudePath {
		t.Fatalf("expected only claude from overrideHome (%q), got %+v", claudePath, res.DiscoveredBuiltin)
	}
	if len(res.IDEPlugins) != 0 {
		t.Fatalf("expected no IDE plugins from host home, got %+v", res.IDEPlugins)
	}

	// Also verify HomeDir override without OverridePATH still never touches host home.
	resHomeOnly := config.ScanLocalAgents(config.ScanOptions{
		HomeDir: overrideHome,
	})
	for _, b := range resHomeOnly.DiscoveredBuiltin {
		if strings.HasPrefix(b.Path, hostHome) {
			t.Errorf("HomeDir override leaked host home builtin %q: %q", b.ID, b.Path)
		}
	}
	for _, c := range resHomeOnly.DiscoveredCustom {
		if strings.HasPrefix(c.Path, hostHome) {
			t.Errorf("HomeDir override leaked host home custom %q: %q", c.ID, c.Path)
		}
	}
	for _, p := range resHomeOnly.IDEPlugins {
		if strings.HasPrefix(p.Path, hostHome) {
			t.Errorf("HomeDir override leaked host home IDE plugin %q: %q", p.Name, p.Path)
		}
	}
}

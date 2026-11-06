package config_test

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestScanLocalAgents_Hermetic(t *testing.T) {
	fakeHome := t.TempDir()
	fakeBinDir := filepath.Join(fakeHome, "fakebin")
	if err := os.MkdirAll(fakeBinDir, 0755); err != nil {
		t.Fatalf("failed to create fake bin dir: %v", err)
	}

	// Helper to create mock executable
	createMockExec := func(dir, name string) string {
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

	// 1. Create mock executables for built-in engines: agy and claude
	agyPath := createMockExec(fakeBinDir, "agy")
	claudePath := createMockExec(fakeBinDir, "claude")

	// 2. Create mock custom CLI: cursor
	cursorPath := createMockExec(fakeBinDir, "cursor")

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

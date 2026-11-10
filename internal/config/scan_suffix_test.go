package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
)

func TestWindowsExecutableSearch_SuffixOrder(t *testing.T) {
	got := config.ExecutableSearchNames("agy", true)
	want := []string{"agy", "agy.exe", "agy.bat", "agy.cmd"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
	exact := config.ExecutableSearchNames("agy.cmd", true)
	if len(exact) != 1 || exact[0] != "agy.cmd" {
		t.Fatalf("suffixed name = %v, want [agy.cmd]", exact)
	}
	if plain := config.ExecutableSearchNames("agy", false); len(plain) != 1 || plain[0] != "agy" {
		t.Fatalf("non-windows names = %v", plain)
	}
}

func TestWindowsExecutableSearch_ExeBeforeBatBeforeCmd(t *testing.T) {
	dir := t.TempDir()
	exe := writeFixture(t, dir, "agy.exe")
	writeFixture(t, dir, "agy.bat")
	writeFixture(t, dir, "agy.cmd")
	got := config.FirstExistingNamed(dir, config.ExecutableSearchNames("agy", true))
	if got != exe {
		t.Fatalf("found %s, want %s", got, exe)
	}
}

func TestWindowsExecutableSearch_BatBeforeCmd(t *testing.T) {
	dir := t.TempDir()
	bat := writeFixture(t, dir, "agy.bat")
	writeFixture(t, dir, "agy.cmd")
	got := config.FirstExistingNamed(dir, config.ExecutableSearchNames("agy", true))
	if got != bat {
		t.Fatalf("found %s, want %s", got, bat)
	}
}

func TestWindowsExecutableSearch_CmdAlone(t *testing.T) {
	dir := t.TempDir()
	cmd := writeFixture(t, dir, "cursor.cmd")
	got := config.FirstExistingNamed(dir, config.ExecutableSearchNames("cursor", true))
	if got != cmd {
		t.Fatalf("found %s, want %s", got, cmd)
	}
}

func TestWindowsExecutableSearch_MissingAndDirectory(t *testing.T) {
	dir := t.TempDir()
	if got := config.FirstExistingNamed(dir, config.ExecutableSearchNames("agy", true)); got != "" {
		t.Fatalf("missing search returned %s", got)
	}
	if err := os.Mkdir(filepath.Join(dir, "agy.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	bat := writeFixture(t, dir, "agy.bat")
	got := config.FirstExistingNamed(dir, config.ExecutableSearchNames("agy", true))
	if got != bat {
		t.Fatalf("directory agy.exe was selected or bat missed: %s", got)
	}
}

func TestWizardDefaultPath_ConfiguredPathKept(t *testing.T) {
	saved := filepath.Join(t.TempDir(), "saved.cmd")
	scanned := filepath.Join(t.TempDir(), "scanned.exe")
	if got := config.WizardDefaultExecutablePath(saved, scanned); got != saved {
		t.Fatalf("configured path = %s, want %s", got, saved)
	}
	if got := config.WizardDefaultExecutablePath("  ", scanned); got != scanned {
		t.Fatalf("empty configured path = %s, want %s", got, scanned)
	}
	if got := config.WizardDefaultExecutablePath("", ""); got != "" {
		t.Fatalf("both empty = %q", got)
	}

	dir := t.TempDir()
	chosen := filepath.Join(dir, "tool.cmd")
	if err := os.WriteFile(chosen, []byte("agentpulse synthetic cmd fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents: []config.AgentConfig{{
			ID:    "cursor",
			Path:  chosen,
			Alias: []string{"cursor"},
		}},
	}
	cfgPath := filepath.Join(dir, "agents.yaml")
	if err := config.SaveConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Agents) != 1 || loaded.Agents[0].Path != chosen || loaded.Agents[0].ID != "cursor" {
		t.Fatalf("loaded agents = %+v, want path %s", loaded.Agents, chosen)
	}
}

func TestProbeAgent_ExplicitSyntheticPathNotExecuted(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "cursor.cmd")
	if err := os.WriteFile(present, []byte("agentpulse synthetic cmd fixture\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(present, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ok := config.ProbeAgentContext(ctx, config.AgentConfig{
		ID:    "cursor",
		Path:  present,
		Alias: []string{"cursor"},
	})
	if !ok.Ok || ok.ResolvedPath != present {
		t.Fatalf("present probe = %+v, want the fixture path and no execution", ok)
	}

	missing := filepath.Join(dir, "missing.cmd")
	bad := config.ProbeAgentContext(ctx, config.AgentConfig{
		ID:   "cursor",
		Path: missing,
	})
	if bad.Ok {
		t.Fatalf("missing path was reported available: %+v", bad)
	}
	if bad.Detail == "" || bad.ResolvedPath != "" {
		t.Fatalf("missing probe = %+v, want an unavailable detail and empty resolved path", bad)
	}
}

func writeFixture(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("agentpulse synthetic fixture\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

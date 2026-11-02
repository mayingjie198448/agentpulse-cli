package cli_test

import (
	"bytes"
	"os"
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
}

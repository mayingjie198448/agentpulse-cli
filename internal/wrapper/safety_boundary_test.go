package wrapper_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/safety"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

func TestWrapperExplicitDangerRefusesWithoutIsolation(t *testing.T) {
	tmp := t.TempDir()
	started := filepath.Join(tmp, "child-started")
	status := filepath.Join(tmp, "status.json")
	audit := filepath.Join(tmp, "audit.jsonl")
	dummyBin := filepath.Join(tmp, "dummy-agent")
	dummyBody := []byte("#!/bin/sh\necho started > \"$1\"\n")
	if runtime.GOOS == "windows" {
		dummyBin += ".cmd"
		dummyBody = []byte("@echo off\r\necho started > \"%~1\"\r\n")
	}
	if err := os.WriteFile(dummyBin, dummyBody, 0755); err != nil {
		t.Fatal(err)
	}
	w, err := wrapper.New(wrapper.Config{
		MachineID:        "node",
		JobID:            "job-guard",
		Engine:           model.EngineAgy,
		Title:            "must not start",
		Args:             []string{dummyBin, started, "--mqtt-password", "s3cr3t-mqtt-password"},
		StatusFile:       status,
		ExplicitDanger:   true,
		AuditPath:        audit,
		IsolationBackend: safety.UnavailableBackend(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = w.Run(ctx)
	if err == nil || !errors.Is(err, safety.ErrRefused) {
		t.Fatalf("wrapper started danger mode without isolation: %v", err)
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatal("placeholder command ran")
	}
	body, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "s3cr3t-mqtt-password") {
		t.Fatalf("audit leaked secret: %s", body)
	}
}

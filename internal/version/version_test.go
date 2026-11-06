package version_test

import (
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/version"
)

func TestVersionInfo(t *testing.T) {
	if version.Version != "0.7.0" {
		t.Fatalf("expected version.Version to be 0.7.0, got %s", version.Version)
	}
	info := version.Info()
	if !strings.Contains(info, "agentpulse 0.7.0") {
		t.Errorf("expected Info() to contain 'agentpulse 0.7.0', got %q", info)
	}
	if !strings.Contains(info, "2026-11-06") {
		t.Errorf("expected Info() to contain BuildDate '2026-11-06', got %q", info)
	}
}

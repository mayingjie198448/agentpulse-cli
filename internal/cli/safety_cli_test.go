package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
)

func TestSafetyCleanupRequiresRunIDAndDoesNotUseHome(t *testing.T) {
	base := t.TempDir()
	keep := filepath.Join(base, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	cmd := cli.NewRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"safety", "cleanup"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("cleanup without --run-id succeeded")
	}
	if body, rerr := os.ReadFile(keep); rerr != nil || string(body) != "keep" {
		t.Fatalf("cleanup used HOME: %v %q", rerr, body)
	}
}

func TestSafetyBackendReportsBlockedSurfaces(t *testing.T) {
	cmd := cli.NewRootCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"safety", "backend"})
	err := cmd.Execute()
	text := buf.String()
	for _, needle := range []string{"BLOCKED", "darwin_sandbox_exec", "credential_agent_ipc_proxy", "windows_write_isolation"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("backend output missing %s (err=%v):\n%s", needle, err, text)
		}
	}
}

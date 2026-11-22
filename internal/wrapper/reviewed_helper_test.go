package wrapper_test

import (
	"os"
	"strings"
	"testing"
)

// reviewedHelperArgv is the only command tests may execute in place of a shell.
// AGENTPULSE_TEST_HELPER must point at the reviewed helper PE. That helper
// prints a fixture line or exits 0. It does not start a child agent.
func reviewedHelperArgv(t *testing.T, mode string) []string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("AGENTPULSE_TEST_HELPER"))
	if path == "" {
		t.Fatal("AGENTPULSE_TEST_HELPER is unset; refusing to execute a shell or /bin/true")
	}
	if mode == "" {
		mode = "true"
	}
	return []string{path, mode}
}

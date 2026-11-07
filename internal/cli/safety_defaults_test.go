package cli

import "testing"

func TestDaemonDoesNotOptInToDangerByDefault(t *testing.T) {
	if defaultDangerouslySkipPermissions {
		t.Fatal("dual-role daemon must not opt in to dangerously-skip-permissions")
	}
}

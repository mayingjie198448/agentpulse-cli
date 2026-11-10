//go:build windows

package wrapper

import (
	"os"
	"testing"
)

// TestWindowsProcessBackend_CurrentPID is the native fixture for this backend.
// It queries only the current test process. It does not walk other users' tasks.
func TestWindowsProcessBackend_CurrentPID(t *testing.T) {
	pid := os.Getpid()
	if !IsProcessAlive(pid) {
		t.Fatal("current process was not observed alive")
	}
	start, ok := ObservedStartTime(pid)
	if !ok || start.IsZero() {
		t.Fatal("current process start time was not observed")
	}
	again, ok := ObservedStartTime(pid)
	if !ok || !again.Equal(start) {
		t.Fatalf("start time changed for the live process: %s then %s", start, again)
	}
	if cwd := ResolveProcessCwd(pid); cwd != "" {
		t.Fatalf("windows cwd must stay empty until a supported read exists, got %q", cwd)
	}
	code, known := observeWindowsExit(pid)
	if known {
		t.Fatalf("live current process reported exit code %d", code)
	}

	procs, err := listDefaultAllowlisted(t.Context(), []int{pid})
	if err != nil {
		t.Fatalf("allowlisted query: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("allowlisted query returned %d processes, want only pid %d: %+v", len(procs), pid, procs)
	}
	got := procs[0]
	if got.PID != pid || !got.StartTime.Equal(start) || got.Cwd != "" || len(got.Args) == 0 || got.Args[0] == "" {
		t.Fatalf("current process record = %+v, want pid %d start %s empty cwd and an observed image", got, pid, start)
	}
}

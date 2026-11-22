//go:build windows

package wrapper

import (
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
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

// TestWindowsProcessBackend_EmptyAllowlistDoesNotOpen proves a nil or empty
// allowlist does not call OpenProcess. The query hook replaces the real
// open, so this test does not open other processes.
func TestWindowsProcessBackend_EmptyAllowlistDoesNotOpen(t *testing.T) {
	opened := hookOpens(t)
	ctx := t.Context()
	for _, call := range []func() ([]ProcessInfo, error){
		func() ([]ProcessInfo, error) { return DefaultProcessLister(ctx) },
		func() ([]ProcessInfo, error) { return listDefaultAllowlisted(ctx, nil) },
		func() ([]ProcessInfo, error) { return listDefaultAllowlisted(ctx, []int{}) },
		func() ([]ProcessInfo, error) { return listDefaultAllowlisted(ctx, []int{0, -1}) },
		func() ([]ProcessInfo, error) { return listWindowsSnapshot(nil) },
		func() ([]ProcessInfo, error) { return listWindowsSnapshot(map[int]bool{}) },
	} {
		procs, err := call()
		if err != nil {
			t.Fatalf("empty query: %v", err)
		}
		if len(procs) != 0 || len(*opened) != 0 {
			t.Fatalf("empty allowlist returned %d rows and opened %v", len(procs), *opened)
		}
	}

	pid := os.Getpid()
	*opened = nil
	procs, err := listDefaultAllowlisted(ctx, []int{pid, 0, -4})
	if err != nil {
		t.Fatalf("allowlisted query: %v", err)
	}
	if len(procs) != 0 {
		t.Fatalf("blocked query returned rows: %+v", procs)
	}
	if len(*opened) == 0 {
		t.Fatal("allowlisted current pid was not queried")
	}
	for _, got := range *opened {
		if int(got) != pid {
			t.Fatalf("opened pid %d outside the allowlist %d", got, pid)
		}
	}
	openProcessQuery = realOpen

	start, ok := ObservedStartTime(pid)
	if !ok {
		t.Fatal("current process start time was not observed")
	}
	code, known := observeWindowsExitFor(pid, start)
	if known {
		t.Fatalf("live current process reported exit code %d", code)
	}
	code, known = observeWindowsExitFor(pid, time.Time{})
	if known || code != 0 {
		t.Fatalf("missing attach identity reported %+v", known)
	}
}

var realOpen = openProcessQuery

func hookOpens(t *testing.T) *[]uint32 {
	t.Helper()
	var opened []uint32
	prev := openProcessQuery
	t.Cleanup(func() { openProcessQuery = prev })
	openProcessQuery = func(pid uint32) (windows.Handle, error) {
		opened = append(opened, pid)
		return 0, errors.New("query blocked")
	}
	return &opened
}

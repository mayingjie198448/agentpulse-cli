package wrapper_test

import (
	"strings"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

// TestParseWindowsProcessCSV pins issue #66: the Win32_Process CIM query
// output must yield PID/PPID, local-wall-clock start time and intact
// Windows command lines (backslashes are path separators, not escapes).
func TestParseWindowsProcessCSV(t *testing.T) {
	prevLocal := time.Local
	time.Local = time.FixedZone("UTC+8", 8*3600)
	t.Cleanup(func() { time.Local = prevLocal })

	fixture := "\"Pid\",\"Ppid\",\"Created\",\"Cmd\"\n" +
		"\"12345\",\"1\",\"2026-11-27 10:00:00\",\"\"\"C:\\Program Files\\agy\\agy.exe\"\" -p task\"\n" +
		"\"4\",\"0\",\"\",\"\"\n"

	procs, err := wrapper.ParseWindowsProcessCSVForTest([]byte(fixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(procs))
	}
	p := procs[0]
	if p.PID != 12345 || p.PPID != 1 {
		t.Fatalf("pid/ppid = %d/%d, want 12345/1", p.PID, p.PPID)
	}
	wantStart := time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)
	if !p.StartTime.Equal(wantStart) {
		t.Fatalf("StartTime = %s, want %s (local 10:00 UTC+8 minus 8h)", p.StartTime.Format(time.RFC3339), wantStart.Format(time.RFC3339))
	}
	if len(p.Args) == 0 || p.Args[0] != `C:\Program Files\agy\agy.exe` {
		t.Fatalf("Args[0] = %q, want intact Windows path", p.Args)
	}
	if procs[1].PID != 4 || len(procs[1].Args) != 0 || !procs[1].StartTime.IsZero() {
		t.Fatalf("system row = %+v, want PID 4 with no args and zero start", procs[1])
	}
}

func TestTokenizeWindowsCommandLineKeepsBackslashes(t *testing.T) {
	got := wrapper.TokenizeWindowsCommandLineForTest(`"C:\Program Files\opencode\opencode.exe" run --prompt "hi there"`)
	want := []string{`C:\Program Files\opencode\opencode.exe`, "run", "--prompt", "hi there"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
}

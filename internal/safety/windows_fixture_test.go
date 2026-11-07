//go:build windows

package safety

import (
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	MaybeHandleInternal(os.Args)
	MaybeFixtureWorker(os.Args)
	os.Exit(m.Run())
}

// TestWindowsNativeFixture is the ops acceptance command on a Windows machine:
//
//	go test -c -o %TEMP%\agentpulse-safety-windows.test ./internal/safety/
//	%TEMP%\agentpulse-safety-windows.test -test.run TestWindowsNativeFixture -test.v
//
// It does not launch agy. Cross-compiling the test is not a pass.
func TestWindowsNativeFixture(t *testing.T) {
	report, err := RunNativeFixture(t.Context())
	t.Log(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "result=PASS") || !strings.Contains(report, "appcontainer=1") {
		t.Fatalf("report:\n%s", report)
	}
	if !strings.Contains(report, "BLOCKED_UNTIL_OPS_EXECUTES") {
		t.Fatalf("catalog flipped to a compile-time pass:\n%s", report)
	}
}

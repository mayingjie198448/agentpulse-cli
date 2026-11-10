//go:build darwin

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

// TestDarwinNativeFixture is the ops acceptance entrypoint on a Darwin machine:
//
//	go test -c -mod=readonly -o /tmp/agentpulse-safety-darwin.test ./internal/safety/
//	/tmp/agentpulse-safety-darwin.test -test.run ^TestDarwinNativeFixture$ -test.v -test.count=1 -test.timeout=30s
//
// It runs under Darwin Seatbelt sandbox-exec against synthetic sentinels only.
// It does not launch agy or touch real user assets. Keychain IPC remains BLOCKED.
func TestDarwinNativeFixture(t *testing.T) {
	report, err := RunNativeFixture(t.Context())
	t.Log(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "result=PASS") {
		t.Fatalf("report did not pass:\n%s", report)
	}
	if !strings.Contains(report, "backend=darwin-sandbox-exec") {
		t.Fatalf("backend was not darwin-sandbox-exec:\n%s", report)
	}
	if !strings.Contains(report, "catalog_darwin_mach_securityd_keychain=BLOCKED") {
		t.Fatalf("keychain catalog flipped unexpectedly:\n%s", report)
	}
}

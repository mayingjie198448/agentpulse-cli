package safety

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startupExe is a synthetic verified-image path. The test never opens it.
const startupExe = "/private/tmp/ap-mini-native/bin/safety-darwin-arm64.test"

const startupRoot = "/private/tmp/agentpulse-runs/startup-channel-root"

// exitStatus is a wait error with an observable process status.
type exitStatus struct{ code int }

func (e exitStatus) Error() string { return "child exited" }

func (e exitStatus) ExitCode() int { return e.code }

func startupProfile(t *testing.T) string {
	t.Helper()
	clearDarwinRegistries()
	restore := setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
		return exePath, 11, 22, true, seamImageHash, nil
	})
	defer restore()
	if _, err := resolveDarwinAllowedReads(RunRecord{
		RunID:     "run-startup-1234567890abcdef1234",
		Root:      startupRoot,
		Nonce:     "nonce-startup-1234567890abcdef12",
		ExeSHA256: seamImageHash,
	}, startupExe, nil); err != nil {
		t.Fatalf("resolve image: %v", err)
	}
	prof, err := DarwinSeatbeltProfile(startupRoot, startupExe)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	return prof
}

func TestDarwinHelperStartupChannelContract(t *testing.T) {
	t.Run("ProfileLetsHelperBootAndReport", func(t *testing.T) {
		prof := startupProfile(t)
		required := []string{
			"(allow sysctl-read)",
			"(allow file-read-data file-write-data file-ioctl (vnode-type FIFO))",
			"(allow file-map-executable (subpath \"/usr/lib\"))",
			"(allow file-map-executable (subpath \"/System/Library\"))",
			"(allow file-map-executable (subpath \"/usr/bin\"))",
			"(allow file-map-executable (subpath \"/bin\"))",
			"(allow file-map-executable (subpath \"/sbin\"))",
			"(allow file-map-executable (subpath \"/private/var/db/dyld\"))",
			"(allow file-map-executable (literal \"/private/tmp/ap-mini-native/bin/safety-darwin-arm64.test\"))",
		}
		for _, line := range required {
			if !strings.Contains(prof, line) {
				t.Errorf("startup profile missing %s", line)
			}
		}
		if strings.Contains(prof, "(vnode-type PIPE)") {
			t.Error("profile uses unbound vnode-type PIPE; sandbox-exec rejects the profile before the helper starts")
		}
		if strings.Contains(prof, "(allow sysctl-write)") {
			t.Error("profile allows sysctl-write")
		}
		if strings.Contains(prof, "(allow file-write*)") {
			t.Error("profile allows unfiltered file-write*")
		}
		for _, keep := range []string{
			"(deny default)",
			"(deny file-write*)",
			"(deny mach-lookup (global-name \"com.apple.securityd\"))",
			"(deny mach-lookup (global-name \"com.apple.SecurityServer\"))",
			"(deny mach-lookup (global-name \"com.apple.ocspd\"))",
			"(deny file-read* (subpath \"/Users\"))",
			"(deny file-write* (subpath \"/Library/Keychains\"))",
			"(deny file-read* (subpath \"/Library/Keychains\"))",
		} {
			if !strings.Contains(prof, keep) {
				t.Errorf("profile dropped %s", keep)
			}
		}
		if strings.Contains(prof, "(allow mach-lookup (global-name \"com.apple.securityd\"))") {
			t.Error("profile allows securityd lookup")
		}
	})

	t.Run("EOFStillRefusesAndRecordsObservedExit", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close write: %v", err)
		}
		p := &Prepared{
			confirmR:  r,
			auditFile: filepath.Join(t.TempDir(), "audit.jsonl"),
			waitFn:    func() error { return exitStatus{code: 9} },
		}
		err = p.readConfirm()
		if err == nil {
			t.Fatal("empty confirm pipe was accepted")
		}
		if !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("error = %q, want the empty-pipe EOF", err.Error())
		}
		if p.audit.IsolationApplied {
			t.Fatal("EOF marked isolation applied")
		}
		if p.audit.ControllerResult != "refused" {
			t.Fatalf("controller = %q", p.audit.ControllerResult)
		}
		if p.audit.ChildExit != nil {
			t.Fatalf("child exit = %v, user command must stay unstarted", *p.audit.ChildExit)
		}
		if p.audit.HelperExit == nil || *p.audit.HelperExit != 9 {
			got := -1
			if p.audit.HelperExit != nil {
				got = *p.audit.HelperExit
			}
			t.Fatalf("helper exit = %d, want observed wait status 9", got)
		}
	})

	t.Run("OKStillRequiredBeforeSuccess", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		if _, err := io.WriteString(w, "OK\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		p := &Prepared{
			confirmR:  r,
			auditFile: filepath.Join(t.TempDir(), "audit.jsonl"),
			waitFn:    func() error { t.Fatal("wait must not run after OK"); return nil },
		}
		if err := p.readConfirm(); err != nil {
			t.Fatalf("OK confirm refused: %v", err)
		}
		if p.audit.ControllerResult == "refused" {
			t.Fatal("OK confirm was recorded as refused")
		}
	})

	t.Run("FailReasonStillRefuses", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		if _, err := io.WriteString(w, "FAIL profile_mismatch\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		p := &Prepared{
			confirmR:  r,
			auditFile: filepath.Join(t.TempDir(), "audit.jsonl"),
			waitFn:    func() error { return exitStatus{code: 78} },
		}
		err = p.readConfirm()
		if err == nil || !strings.Contains(err.Error(), "profile_mismatch") {
			t.Fatalf("FAIL confirm error = %v", err)
		}
		if p.audit.IsolationApplied || p.audit.ControllerResult != "refused" {
			t.Fatalf("FAIL confirm applied=%v result=%s", p.audit.IsolationApplied, p.audit.ControllerResult)
		}
	})
}

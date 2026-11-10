package safety

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

func TestDarwinSeatbeltProfilePolicyContract(t *testing.T) {
	root := "/private/tmp/agentpulse-runs/test-run-root"
	prof, err := DarwinSeatbeltProfile(root)
	if err != nil {
		t.Fatalf("unexpected profile creation error: %v", err)
	}

	requiredSubstrings := []string{
		"(version 1)",
		"(deny default)",
		"(allow process-exec)",
		"(allow process-fork)",
		"(allow signal (target self))",
		"(allow file-read*)",
		"(allow file-ioctl (literal \"/dev/null\"))",
		"(allow file-write* (literal \"/dev/null\"))",
		"(deny file-write*)",
		"(allow file-write* (subpath \"/private/tmp/agentpulse-runs/test-run-root\"))",
		"(deny mach-lookup (global-name \"com.apple.SecurityServer\"))",
		"(deny mach-lookup (global-name \"com.apple.securityd\"))",
		"(deny mach-lookup (global-name \"com.apple.ocspd\"))",
	}
	for _, req := range requiredSubstrings {
		if !strings.Contains(prof, req) {
			t.Errorf("seatbelt profile missing mandatory directive %q", req)
		}
	}

	// Broad permissions must be rejected
	for _, forbidden := range []string{
		"(allow default)",
		"(allow network*)",
		"(allow mach-lookup (global-name \"com.apple.securityd\"))",
		"(allow file-write* (subpath \"/Users",
	} {
		if strings.Contains(prof, forbidden) {
			t.Errorf("seatbelt profile illegally contains broad permission %q", forbidden)
		}
	}

	// Refusal on injection characters
	for _, bad := range []string{
		"/private/tmp/bad\"path",
		"/private/tmp/bad(path",
		"/private/tmp/bad)path",
		"/private/tmp/bad\\path",
		"relative/path",
		"",
		"   ",
	} {
		if _, err := DarwinSeatbeltProfile(bad); err == nil {
			t.Errorf("expected error for dangerous path %q, got nil", bad)
		}
	}
}

func TestDarwinPathNormalizationContract(t *testing.T) {
	// Canonical path requirements
	if err := validateDarwinCanonicalPath("/private/tmp/agentpulse-runs/run1"); err != nil {
		t.Fatalf("valid canonical path rejected: %v", err)
	}
	if err := validateDarwinCanonicalPath("/tmp/agentpulse-runs/run1"); err == nil {
		t.Fatal("non-canonical /tmp path should be rejected")
	}
	if err := validateDarwinCanonicalPath("/var/agentpulse-runs/run1"); err == nil {
		t.Fatal("non-canonical /var path should be rejected")
	}
	if err := validateDarwinCanonicalPath("/private/tmp/run(test)"); err == nil {
		t.Fatal("path with '(' should be rejected")
	}

	// Sentinel within root must be detectable
	root := "/private/tmp/agentpulse-runs/run1"
	insideSentinel := "/private/tmp/agentpulse-runs/run1/work/sentinel"
	outsideSentinel := "/private/tmp/agentpulse-sentinels/sentinel1"

	if !withinPath(root, insideSentinel) {
		t.Fatal("inside sentinel was not detected as within root")
	}
	if withinPath(root, outsideSentinel) {
		t.Fatal("outside sentinel was falsely detected as within root")
	}

	// Protected home protection
	fakeHome := "/Users/testuser"
	if !withinPath(fakeHome, "/Users/testuser/.ssh/id_ed25519") {
		t.Fatal("home asset not detected as within protected home")
	}
}

func TestDarwinLaunchArgvContract(t *testing.T) {
	sandboxExec := "/usr/bin/sandbox-exec"
	profile := "(version 1)(deny default)"
	selfExe := "/private/tmp/bin/agentpulse"
	isolateArgs := []string{ReexecArg, "--run-root", "/private/tmp/run1", "--", "echo", "hello"}

	argv, err := buildDarwinSandboxExecArgv(sandboxExec, profile, selfExe, isolateArgs)
	if err != nil {
		t.Fatalf("buildDarwinSandboxExecArgv failed: %v", err)
	}

	if len(argv) < 5 {
		t.Fatalf("argv too short: %v", argv)
	}
	if argv[0] != "/usr/bin/sandbox-exec" {
		t.Errorf("argv[0] want %q, got %q", sandboxExec, argv[0])
	}
	if argv[1] != "-p" || argv[2] != profile {
		t.Errorf("argv profile flag mismatch: %v", argv[1:3])
	}
	if argv[3] != selfExe {
		t.Errorf("argv target executable mismatch: %q", argv[3])
	}
	if argv[4] != ReexecArg {
		t.Errorf("argv reexec arg mismatch: %q", argv[4])
	}

	// Missing inputs must fail closed
	if _, err := buildDarwinSandboxExecArgv("", profile, selfExe, isolateArgs); err == nil {
		t.Fatal("expected error for empty sandboxExec")
	}
	if _, err := buildDarwinSandboxExecArgv(sandboxExec, "", selfExe, isolateArgs); err == nil {
		t.Fatal("expected error for empty profile")
	}
	if _, err := buildDarwinSandboxExecArgv(sandboxExec, profile, "", isolateArgs); err == nil {
		t.Fatal("expected error for empty selfExe")
	}
}

func TestDarwinSentinelProbeContract(t *testing.T) {
	sentinel := "/private/tmp/sentinel"
	before := []byte("sentinel-v1\n")
	afterSame := []byte("sentinel-v1\n")
	afterTampered := []byte("hacked\n")

	// Happy path: all operations denied with EPERM, content unchanged
	err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, syscall.EPERM, syscall.EPERM, before, afterSame)
	if err != nil {
		t.Fatalf("unexpected probe failure on EPERM denials: %v", err)
	}

	// Happy path: EACCES denial
	err = checkDarwinSentinelProbe(sentinel, syscall.EACCES, syscall.EACCES, syscall.EACCES, before, afterSame)
	if err != nil {
		t.Fatalf("unexpected probe failure on EACCES denials: %v", err)
	}

	// Unhappy path: write was permitted (nil error)
	if err := checkDarwinSentinelProbe(sentinel, nil, syscall.EPERM, syscall.EPERM, before, afterSame); err == nil {
		t.Fatal("probe must fail if write was not denied")
	}

	// Unhappy path: remove was permitted (nil error)
	if err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, nil, syscall.EPERM, before, afterSame); err == nil {
		t.Fatal("probe must fail if remove was not denied")
	}

	// Unhappy path: sibling creation was permitted (nil error)
	if err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, syscall.EPERM, nil, before, afterSame); err == nil {
		t.Fatal("probe must fail if sibling creation was not denied")
	}

	// Unhappy path: content was changed
	if err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, syscall.EPERM, syscall.EPERM, before, afterTampered); err == nil {
		t.Fatal("probe must fail if sentinel content changed")
	}
}

func TestDarwinConfirmPipeContract(t *testing.T) {
	// Positive OK confirmation
	ok, reason, err := parseDarwinConfirmMessage("OK\n")
	if !ok || reason != "OK" || err != nil {
		t.Fatalf("parseDarwinConfirmMessage(OK) failed: ok=%v reason=%s err=%v", ok, reason, err)
	}

	// Negative FAIL confirmation
	ok, reason, err = parseDarwinConfirmMessage("FAIL root_identity\n")
	if ok || reason != "root_identity" || err == nil {
		t.Fatalf("parseDarwinConfirmMessage(FAIL) unexpected: ok=%v reason=%s err=%v", ok, reason, err)
	}

	// Truncated/closed pipe
	ok, reason, err = parseDarwinConfirmMessage("")
	if ok || !strings.Contains(reason, "closed confirm pipe") || err == nil {
		t.Fatalf("parseDarwinConfirmMessage('') unexpected: ok=%v reason=%s err=%v", ok, reason, err)
	}
}

func TestDarwinCleanupMarkerContract(t *testing.T) {
	rec := RunRecord{
		RunID: "0123456789abcdef0123456789abcdef",
		Root:  "/private/tmp/agentpulse-runs/run1",
		Nonce: "testnonce1234",
		Dev:   100,
		Ino:   200,
	}

	validMarker := RunRecord{
		RunID: "0123456789abcdef0123456789abcdef",
		Root:  "/private/tmp/agentpulse-runs/run1",
		Nonce: "testnonce1234",
		Dev:   100,
		Ino:   200,
	}

	if err := validateDarwinCleanupMarker(validMarker, rec, 100, 200); err != nil {
		t.Fatalf("valid marker rejected: %v", err)
	}

	// Mismatched run ID
	badRunID := validMarker
	badRunID.RunID = "fedcba9876543210fedcba9876543210"
	if err := validateDarwinCleanupMarker(badRunID, rec, 100, 200); err == nil || !errors.Is(err, ErrCleanupBlocked) {
		t.Fatalf("expected ErrCleanupBlocked for run ID mismatch, got %v", err)
	}

	// Mismatched nonce
	badNonce := validMarker
	badNonce.Nonce = "wrongnonce"
	if err := validateDarwinCleanupMarker(badNonce, rec, 100, 200); err == nil || !errors.Is(err, ErrCleanupBlocked) {
		t.Fatalf("expected ErrCleanupBlocked for nonce mismatch, got %v", err)
	}

	// Mismatched Ino
	if err := validateDarwinCleanupMarker(validMarker, rec, 100, 999); err == nil || !errors.Is(err, ErrCleanupBlocked) {
		t.Fatalf("expected ErrCleanupBlocked for inode mismatch, got %v", err)
	}
}

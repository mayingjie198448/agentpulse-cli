package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLeakDetection_RawSecretFailsRun verifies that raw secret leaks on any channel
// (stdout, stderr, status.json, audit.jsonl) reliably record failures and fail the run.
func TestLeakDetection_RawSecretFailsRun(t *testing.T) {
	ev := &evidence{}

	// Clean text passes
	probeLeakedSecrets(ev, "clean channel", "hello world ordinary log")
	if len(ev.Failures) != 0 || len(ev.LeakFailures) != 0 {
		t.Fatalf("expected no failure on clean text, got %v", ev.Failures)
	}

	// Secret leak on stdout must fail
	probeLeakedSecrets(ev, "listener stdout", "something password=synthetic-secret here")
	if len(ev.LeakFailures) != 1 || !strings.Contains(ev.LeakFailures[0], "listener stdout") {
		t.Fatalf("expected leak failure on listener stdout, got %v", ev.LeakFailures)
	}
	if len(ev.Failures) != 1 || !strings.Contains(ev.Failures[0], "listener stdout") {
		t.Fatalf("expected overall failure on listener stdout, got %v", ev.Failures)
	}

	// Secret leak on stderr must fail
	probeLeakedSecrets(ev, "listener stderr", "bearer-token=ghp_ABC123456789012345678901234567890123456")
	if len(ev.LeakFailures) != 2 || !strings.Contains(ev.LeakFailures[1], "listener stderr") {
		t.Fatalf("expected leak failure on listener stderr, got %v", ev.LeakFailures)
	}

	// Artifact file leak detection
	tmpDir := t.TempDir()
	leakFile := filepath.Join(tmpDir, "leaked.json")
	if err := os.WriteFile(leakFile, []byte(`{"data": "synthetic-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	probeRawArtifactLeak(ev, "test_artifact", leakFile)
	if len(ev.LeakFailures) != 3 || !strings.Contains(ev.LeakFailures[2], "test_artifact") {
		t.Fatalf("expected leak failure on test_artifact, got %v", ev.LeakFailures)
	}
}

// TestScrubString_KeepsScrubbedValue verifies that scrubString does not discard the
// replacement and scrubs the synthetic secret cleanly.
func TestScrubString_KeepsScrubbedValue(t *testing.T) {
	raw := "line with synthetic-secret and password=supersecret"
	clean, changed := scrubString(raw)
	if !changed {
		t.Fatal("expected changed to be true")
	}
	if strings.Contains(clean, "synthetic-secret") || strings.Contains(clean, "supersecret") {
		t.Fatalf("scrubbed string still contains secret: %q", clean)
	}
	if !strings.Contains(clean, "[SCRUBBED_SYNTHETIC]") {
		t.Fatalf("expected [SCRUBBED_SYNTHETIC] in scrubbed string, got: %q", clean)
	}
	if !strings.Contains(clean, "password=[REDACTED]") {
		t.Fatalf("expected password=[REDACTED] in scrubbed string, got: %q", clean)
	}

	// Clean input returns unchanged
	clean2, changed2 := scrubString("all safe here")
	if changed2 || clean2 != "all safe here" {
		t.Fatalf("expected unchanged for clean string, got %q, changed=%v", clean2, changed2)
	}
}

// TestRescanArtifacts_FailsOnDiskLeak verifies that rescanArtifactsForSecret detects
// unredacted secrets remaining on disk.
func TestRescanArtifacts_FailsOnDiskLeak(t *testing.T) {
	tmpDir := t.TempDir()
	if res := rescanArtifactsForSecret(tmpDir); res != "clean" {
		t.Fatalf("expected clean for empty dir, got %s", res)
	}

	// Create dirty file
	dirtyLog := filepath.Join(tmpDir, "listener.stdout.log")
	if err := os.WriteFile(dirtyLog, []byte("password=synthetic-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := rescanArtifactsForSecret(tmpDir)
	if res == "clean" || !strings.Contains(res, "listener.stdout.log") {
		t.Fatalf("expected rescan to detect dirty listener.stdout.log, got %s", res)
	}
}

// TestDirectoryEscape_JunctionAndSymlinkRefused verifies that symlinks, directory links,
// path traversal escapes, and outside roots are rejected before creation.
func TestDirectoryEscape_JunctionAndSymlinkRefused(t *testing.T) {
	rawTmp := t.TempDir()
	tmpDir, err := filepath.EvalSymlinks(rawTmp)
	if err != nil {
		tmpDir = rawTmp
	}
	ws := filepath.Join(tmpDir, "workspace")
	if err := os.Mkdir(ws, 0o700); err != nil {
		t.Fatal(err)
	}

	// 1. Valid descendant root
	validRoot := filepath.Join(ws, "run-1")
	if !validWorkspace(ws, validRoot) {
		t.Fatalf("expected validWorkspace to accept %q under %q", validRoot, ws)
	}

	// 2. Escape via ".."
	escapeRoot := filepath.Join(ws, "..", "escape")
	if validWorkspace(ws, escapeRoot) {
		t.Fatalf("expected validWorkspace to reject escape root %q", escapeRoot)
	}

	// 3. Root identical to workspace
	if validWorkspace(ws, ws) {
		t.Fatalf("expected validWorkspace to reject root identical to workspace")
	}

	// 4. Coarse root refusals: "/" and empty
	if validWorkspace("/", filepath.Join("/", "something")) {
		t.Fatal("expected validWorkspace to reject / workspace")
	}

	// 5. Symlink hop in chain must be refused
	linkDir := filepath.Join(tmpDir, "symlink-dir")
	if err := os.Symlink(ws, linkDir); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	symlinkedRoot := filepath.Join(linkDir, "sub")
	if validWorkspace(ws, symlinkedRoot) {
		t.Fatalf("expected validWorkspace to reject root through symlink dir %q", symlinkedRoot)
	}
}

// TestExitCodeClassification verifies raw exit codes are never rewritten, non-zero
// codes are never marked clean, and classes are correctly assigned.
func TestExitCodeClassification(t *testing.T) {
	cases := []struct {
		code      int
		wantClean bool
		wantClass string
	}{
		{code: 0, wantClean: true, wantClass: "normal"},
		{code: 1, wantClean: false, wantClass: "control-event"},
		{code: 7, wantClean: false, wantClass: "control-event"},
		{code: 128, wantClean: false, wantClass: "control-event"},
		{code: 137, wantClean: false, wantClass: "abnormal"},
		{code: -1, wantClean: false, wantClass: "unknown"},
		{code: -1073741510, wantClean: false, wantClass: "force-terminate"},
		{code: 3221225786, wantClean: false, wantClass: "force-terminate"},
	}

	for _, tc := range cases {
		clean := isCleanListenerExit(tc.code)
		class := listenerExitClass(tc.code)
		if clean != tc.wantClean {
			t.Errorf("code %d: isCleanListenerExit = %v, want %v", tc.code, clean, tc.wantClean)
		}
		if class != tc.wantClass {
			t.Errorf("code %d: listenerExitClass = %q, want %q", tc.code, class, tc.wantClass)
		}
	}
}

// TestAuditHelperExit_Parsing verifies that auditHelperExit extracts helper and child
// exits from audit.jsonl and reads status.json without killing historical PIDs.
func TestAuditHelperExit_Parsing(t *testing.T) {
	tmpDir := t.TempDir()
	auditDir := filepath.Join(tmpDir, "tmp", "agentpulse-audit")
	if err := os.MkdirAll(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auditFile := filepath.Join(auditDir, "audit.jsonl")

	line0 := `{"timestamp_utc":"2026-11-22T08:00:00Z","argv":["/bin/helper","stream","0"],"child_exit":0,"controller_result":"not_isolated","exit_code":0}`
	line7 := `{"timestamp_utc":"2026-11-22T08:00:01Z","argv":["/bin/helper","stream","7"],"child_exit":7,"controller_result":"not_isolated","exit_code":7}`
	if err := os.WriteFile(auditFile, []byte(line0+"\n"+line7+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	statusFile := filepath.Join(tmpDir, "status.json")
	statusContent := `{"job":{"id":"job-e2e-ok"},"pid":99999}`
	if err := os.WriteFile(statusFile, []byte(statusContent), 0o600); err != nil {
		t.Fatal(err)
	}

	ev := &evidence{}
	job0 := &jobProof{JobID: "job-e2e-ok"}
	auditHelperExit(tmpDir, job0, 0, ev)
	if !job0.ChildExitKnown || job0.ChildExit != 0 {
		t.Fatalf("expected job0 child_exit 0, got %v (%d)", job0.ChildExitKnown, job0.ChildExit)
	}
	if !job0.HelperExitKnown || job0.HelperExit != 0 {
		t.Fatalf("expected job0 helper_exit 0, got %v (%d)", job0.HelperExitKnown, job0.HelperExit)
	}
	if job0.CapturedPID != 99999 {
		t.Fatalf("expected captured PID 99999 from status.json, got %d", job0.CapturedPID)
	}

	job7 := &jobProof{JobID: "job-e2e-fail"}
	auditHelperExit(tmpDir, job7, 7, ev)
	if !job7.ChildExitKnown || job7.ChildExit != 7 {
		t.Fatalf("expected job7 child_exit 7, got %v (%d)", job7.ChildExitKnown, job7.ChildExit)
	}
	if !job7.HelperExitKnown || job7.HelperExit != 7 {
		t.Fatalf("expected job7 helper_exit 7, got %v (%d)", job7.HelperExitKnown, job7.HelperExit)
	}

	// Missing stream code must fail
	jobMissing := &jobProof{JobID: "job-missing"}
	auditHelperExit(tmpDir, jobMissing, 99, ev)
	if jobMissing.HelperExitKnown || jobMissing.ChildExitKnown {
		t.Fatalf("expected missing job to be unknown")
	}
	if len(ev.Failures) == 0 {
		t.Fatalf("expected failure recorded for missing job")
	}
}

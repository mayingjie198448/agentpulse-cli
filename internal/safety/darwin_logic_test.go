package safety

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestDarwinSeatbeltProfilePolicyContract(t *testing.T) {
	clearDarwinRegistries()
	defer clearDarwinRegistries()

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
		"(deny file-write*)",
		"(allow file-write* (subpath \"/private/tmp/agentpulse-runs/test-run-root\"))",
		"(allow file-read* (subpath \"/private/tmp/agentpulse-runs/test-run-root\"))",
		"(allow file-read* (subpath \"/System/Library\"))",
		"(allow file-read* (subpath \"/usr/lib\"))",
		"(allow file-read* (subpath \"/usr/share\"))",
		"(allow file-read* (subpath \"/private/var/db/dyld\"))",
		"(allow file-ioctl (literal \"/dev/null\"))",
		"(allow file-write* (literal \"/dev/null\"))",
		"(allow sysctl-read)",
		"(allow file-read-data file-write-data file-ioctl (vnode-type FIFO))",
		"(allow file-map-executable (subpath \"/usr/lib\"))",
		"(allow file-map-executable (subpath \"/System/Library\"))",
		"(deny mach-lookup (global-name \"com.apple.SecurityServer\"))",
		"(deny mach-lookup (global-name \"com.apple.securityd\"))",
		"(deny mach-lookup (global-name \"com.apple.ocspd\"))",
		"(deny file-read* (subpath \"/Users\"))",
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
		"(allow file-read*)",
		"(allow file-read* (subpath \"/System\")",
		"(allow file-read* (subpath \"/usr\")",
		"(allow file-read* (subpath \"/private/var\")",
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

	// Allowed readable literal: safe external synthetic sentinel, issued only
	// by the exclusive FD chain plus one-run bind.
	safeProbe := "/private/tmp/agentpulse-sentinels/sentinel-12345678"
	authorizeProbeFromExclusiveFD(t, "run1", root, "nonce1", safeProbe, 100, 200)
	profWithProbe, err := DarwinSeatbeltProfile(root, safeProbe)
	if err != nil {
		t.Fatalf("unexpected error adding safe probe literal: %v", err)
	}
	wantLit := "(allow file-read* (literal \"/private/tmp/agentpulse-sentinels/sentinel-12345678\"))"
	if !strings.Contains(profWithProbe, wantLit) {
		t.Errorf("seatbelt profile missing safe read literal %q", wantLit)
	}

	// Allowed readable literal: image proof comes only from the resolver's
	// inspector result, which requires a non-empty run, root, and nonce.
	validExe := "/usr/bin/agentpulse"
	restoreImage := setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
		return path.Clean(exePath), 10, 20, true, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil
	})
	defer restoreImage()
	if _, err := resolveDarwinAllowedReads(RunRecord{
		RunID:     "run1",
		Root:      root,
		Nonce:     "nonce1",
		ExeSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}, validExe, nil); err != nil {
		t.Fatalf("resolver did not issue the image proof: %v", err)
	}
	profWithExe, err := DarwinSeatbeltProfile(root, validExe)
	if err != nil {
		t.Fatalf("unexpected error adding validated image literal: %v", err)
	}
	wantExeLit := "(allow file-read* (literal \"/usr/bin/agentpulse\"))"
	if !strings.Contains(profWithExe, wantExeLit) {
		t.Errorf("seatbelt profile missing validated image literal %q", wantExeLit)
	}
	// Ancestor directory literals of root must be present to permit traversal
	for _, anc := range []string{"/", "/private", "/private/tmp", "/private/tmp/agentpulse-runs"} {
		wantAnc := fmt.Sprintf("(allow file-read* (literal %q))", anc)
		if !strings.Contains(profWithProbe, wantAnc) {
			t.Errorf("seatbelt profile missing ancestor literal %q", wantAnc)
		}
	}

	// Refuse whitelisting protected user/credential paths
	for _, forbiddenLit := range []string{
		"/Users/alice/.ssh/id_ed25519",
		"/Users/alice/.config/agentpulse/config.yaml",
		"/Users/alice/Library/Keychains/login.keychain-db",
		"/System/Volumes/Data/Users/alice/.ssh/id_rsa",
		"/private/var/root/.ssh/id_rsa",
		"/private/tmp/bad\"literal",
		"",
		"relative/path",
		"/Volumes/custom-home/alice/private.txt",
		"/private/tmp/not-created-by-this-run.txt",
	} {
		if _, err := DarwinSeatbeltProfile(root, forbiddenLit); err == nil {
			t.Errorf("expected error whitelisting forbidden read literal %q, got nil", forbiddenLit)
		}
	}
}

func TestDarwinPathNormalizationContract(t *testing.T) {
	t.Run("CanonicalPaths", func(t *testing.T) {
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
	})

	t.Run("SentinelDetection", func(t *testing.T) {
		root := "/private/tmp/agentpulse-runs/run1"
		insideSentinel := "/private/tmp/agentpulse-runs/run1/work/sentinel"
		outsideSentinel := "/private/tmp/agentpulse-sentinels/sentinel1"

		if !withinPath(root, insideSentinel) {
			t.Fatal("inside sentinel was not detected as within root")
		}
		if withinPath(root, outsideSentinel) {
			t.Fatal("outside sentinel was falsely detected as within root")
		}

		fakeHome := "/Users/testuser"
		if !withinPath(fakeHome, "/Users/testuser/.ssh/id_ed25519") {
			t.Fatal("home asset not detected as within protected home")
		}
	})

	t.Run("ProtectedHomePlannerRejectsBeforeMutation", func(t *testing.T) {
		fakeHomes := []string{"/Users/alice", "/Users/bob"}
		statDir := func(path string) (bool, bool, error) {
			return true, true, nil
		}
		target := "/Users/alice/runs/newdir"
		_, err := planDarwinSafeDir(target, fakeHomes, statDir)
		if err == nil {
			t.Fatal("expected planDarwinSafeDir to reject target inside protected home")
		}
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("expected ErrRefused, got %v", err)
		}
	})

	t.Run("ExistingAncestorResolutionAndProtection", func(t *testing.T) {
		fakeHomes := []string{"/Users/alice"}
		statDir := func(path string) (bool, bool, error) {
			if path == "/Users/alice" {
				return true, true, nil
			}
			return false, false, nil
		}
		_, err := planDarwinSafeDir("/Users/alice/sub1/sub2", fakeHomes, statDir)
		if err == nil {
			t.Fatal("expected rejection when ancestor is in protected home")
		}
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("expected ErrRefused, got %v", err)
		}
	})

	t.Run("InvalidTargetRefusedBeforeMutation", func(t *testing.T) {
		statDir := func(path string) (bool, bool, error) {
			return true, true, nil
		}
		for _, bad := range []string{
			"/private/tmp/bad\"dir",
			"/private/tmp/bad(dir)",
			"/tmp/runs",
			"/",
			"",
			"relative/path",
		} {
			_, err := planDarwinSafeDir(bad, []string{"/Users/alice"}, statDir)
			if err == nil {
				t.Fatalf("expected rejection for invalid target %q", bad)
			}
			if !errors.Is(err, ErrRefused) && !errors.Is(err, ErrCleanupBlocked) {
				t.Fatalf("expected refusal error for %q, got %v", bad, err)
			}
		}
	})

	t.Run("OwnedTempPlanSuccessWithMutations", func(t *testing.T) {
		statDir := func(path string) (bool, bool, error) {
			if path == "/private/tmp" {
				return true, true, nil
			}
			return false, false, nil
		}
		target := "/private/tmp/agentpulse-runs/run123"
		plan, err := planDarwinSafeDir(target, []string{"/Users/alice"}, statDir)
		if err != nil {
			t.Fatalf("unexpected plan error: %v", err)
		}
		if plan.ExistingAncestor != "/private/tmp" {
			t.Fatalf("expected ExistingAncestor '/private/tmp', got %q", plan.ExistingAncestor)
		}
		if len(plan.MissingSegments) != 2 || plan.MissingSegments[0] != "agentpulse-runs" || plan.MissingSegments[1] != "run123" {
			t.Fatalf("unexpected missing segments: %v", plan.MissingSegments)
		}

		var mutations []string
		mutator := func(parent, seg string) error {
			mutations = append(mutations, path.Join(parent, seg))
			return nil
		}
		if err := executeDarwinSafeDirPlan(plan, mutator); err != nil {
			t.Fatalf("unexpected execute error: %v", err)
		}
		if len(mutations) != 2 {
			t.Fatalf("expected 2 mutations, got %d", len(mutations))
		}
		if path.Clean(mutations[0]) != path.Clean("/private/tmp/agentpulse-runs") {
			t.Errorf("mutation 0 want /private/tmp/agentpulse-runs, got %q", mutations[0])
		}
		if path.Clean(mutations[1]) != path.Clean("/private/tmp/agentpulse-runs/run123") {
			t.Errorf("mutation 1 want /private/tmp/agentpulse-runs/run123, got %q", mutations[1])
		}
	})

	t.Run("ProvesZeroMutationsOnRejection", func(t *testing.T) {
		var mutations []string
		mutator := func(parent, seg string) error {
			mutations = append(mutations, path.Join(parent, seg))
			return nil
		}
		statDir := func(path string) (bool, bool, error) {
			return true, true, nil
		}
		plan, err := planDarwinSafeDir("/Users/victim/.ssh", []string{"/Users/victim"}, statDir)
		if err == nil {
			_ = executeDarwinSafeDirPlan(plan, mutator)
			t.Fatal("expected plan failure for protected path")
		}
		if len(mutations) != 0 {
			t.Fatalf("expected zero mutations on rejection, got %d (%v)", len(mutations), mutations)
		}
	})
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

	t.Run("SentinelProbeEPERM", func(t *testing.T) {
		err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, syscall.EPERM, syscall.EPERM, before, afterSame)
		if err != nil {
			t.Fatalf("unexpected probe failure on EPERM denials: %v", err)
		}
	})

	t.Run("SentinelProbeEACCES", func(t *testing.T) {
		err := checkDarwinSentinelProbe(sentinel, syscall.EACCES, syscall.EACCES, syscall.EACCES, before, afterSame)
		if err != nil {
			t.Fatalf("unexpected probe failure on EACCES denials: %v", err)
		}
	})

	t.Run("SentinelProbeUnhappy", func(t *testing.T) {
		if err := checkDarwinSentinelProbe(sentinel, nil, syscall.EPERM, syscall.EPERM, before, afterSame); err == nil {
			t.Fatal("probe must fail if write was not denied")
		}
		if err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, nil, syscall.EPERM, before, afterSame); err == nil {
			t.Fatal("probe must fail if remove was not denied")
		}
		if err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, syscall.EPERM, nil, before, afterSame); err == nil {
			t.Fatal("probe must fail if sibling creation was not denied")
		}
		if err := checkDarwinSentinelProbe(sentinel, syscall.EPERM, syscall.EPERM, syscall.EPERM, before, afterTampered); err == nil {
			t.Fatal("probe must fail if sentinel content changed")
		}
	})

	t.Run("FixtureLinkPlanValidation", func(t *testing.T) {
		base := "/private/tmp/fixture-base"
		root := "/private/tmp/fixture-base/runs/run1"
		plan, err := planDarwinFixtureLink(base, root)
		if err != nil {
			t.Fatalf("unexpected error planning fixture link: %v", err)
		}
		if !withinPath(base, plan.ExternalTargetDir) {
			t.Fatalf("expected external target in base, got %q", plan.ExternalTargetDir)
		}
		if withinPath(root, plan.ExternalTargetDir) {
			t.Fatalf("external target %q illegally inside run root %q", plan.ExternalTargetDir, root)
		}
		if !withinPath(root, plan.LinkInWork) {
			t.Fatalf("link %q must be inside run root %q", plan.LinkInWork, root)
		}
	})

	t.Run("FixtureLinkEscapeDenialAndTargetRetention", func(t *testing.T) {
		plan := DarwinFixtureLinkPlan{
			RunRoot:           "/private/tmp/base/runs/run1",
			ExternalTargetDir: "/private/tmp/base/synthetic-external-target",
			ExternalPayload:   "/private/tmp/base/synthetic-external-target/target.txt",
			LinkInWork:        "/private/tmp/base/runs/run1/work/dir-link-escape",
			EscapeThroughLink: "/private/tmp/base/runs/run1/work/dir-link-escape/escape-attempt.txt",
		}
		escapeOutput := "path=/private/tmp/base/runs/run1/work/dir-link-escape/escape-attempt.txt\nwrite=eperm\nremove=eperm\nread=eperm\n"
		beforeIdent := DarwinTargetIdentity{Dev: 100, Ino: 500}
		afterIdent := DarwinTargetIdentity{Dev: 100, Ino: 500}
		payload := []byte("outside-target-v1\n")

		// Happy path: link denied, outside target retained and identical, root and link reclaimed
		err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, beforeIdent, afterIdent, payload, false, false, true)
		if err != nil {
			t.Fatalf("unexpected verification error on valid fixture outcome: %v", err)
		}

		// Negative: write through link permitted
		badEscape := "path=/private/tmp/base/runs/run1/work/dir-link-escape/escape-attempt.txt\nwrite=nil\nremove=eperm\nread=eperm\n"
		if err := verifyDarwinFixtureLinkEscape(badEscape, plan, beforeIdent, afterIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error when link write was not denied")
		}

		// Negative: other paths denied with eperm but link write permitted (R3 mixed counterexample)
		mixedEscape := "path=/Users/testuser/.ssh/id_ed25519\nwrite=eperm\nremove=eperm\nread=eperm\n" +
			"path=/Users/testuser/.config/agentpulse/config.yaml\nwrite=eperm\nremove=eperm\nread=eperm\n" +
			"path=/Users/testuser/Library/Keychains/login.keychain-db\nwrite=eperm\nremove=eperm\nread=eperm\n" +
			"path=/Users/testuser/Library/CloudStorage/OneDrive-Personal/doc.txt\nwrite=eperm\nremove=eperm\nread=eperm\n" +
			"path=/Users/testuser/OneDrive/file.txt\nwrite=eperm\nremove=eperm\nread=eperm\n" +
			"path=/Users/testuser/Desktop/file.txt\nwrite=eperm\nremove=eperm\nread=eperm\n" +
			"path=/Users/testuser/Documents/file.txt\nwrite=eperm\nremove=eperm\nread=eperm\n" +
			"path=" + plan.EscapeThroughLink + "\nwrite=nil\nremove=nil\nread=nil\n"
		if err := verifyDarwinFixtureLinkEscape(mixedEscape, plan, beforeIdent, afterIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error on mixed escape where link write was allowed despite other eperm denials")
		}

		// Negative R3: duplicate field reversal in same record
		dupReversal := "path=" + plan.EscapeThroughLink + "\nwrite=nil\nremove=eperm\nread=eperm\nwrite=eperm\n"
		if err := verifyDarwinFixtureLinkEscape(dupReversal, plan, beforeIdent, afterIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error on duplicate field reversal (write=nil then write=eperm)")
		}

		// Negative R3: missing read outcome
		missingRead := "path=" + plan.EscapeThroughLink + "\nwrite=eperm\nremove=eperm\n"
		if err := verifyDarwinFixtureLinkEscape(missingRead, plan, beforeIdent, afterIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error when read outcome is missing from link record")
		}

		// Negative R3: unknown field in record
		unknownField := "path=" + plan.EscapeThroughLink + "\nwrite=eperm\nremove=eperm\nread=eperm\nunknown=123\n"
		if err := verifyDarwinFixtureLinkEscape(unknownField, plan, beforeIdent, afterIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error on unknown field in escape record")
		}

		// Negative: link record missing entirely
		missingLink := "path=/Users/testuser/.ssh/id_ed25519\nwrite=eperm\nremove=eperm\nread=eperm\n"
		if err := verifyDarwinFixtureLinkEscape(missingLink, plan, beforeIdent, afterIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error when link escape record is missing")
		}

		// Negative: zero identity before or after
		zeroIdent := DarwinTargetIdentity{Dev: 0, Ino: 0}
		if err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, zeroIdent, afterIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error when before identity is zero")
		}
		if err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, beforeIdent, zeroIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error when after identity is zero")
		}

		// Negative: target destroyed
		if err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, beforeIdent, afterIdent, payload, false, false, false); err == nil {
			t.Fatal("expected error when outside target was destroyed")
		}

		// Negative: target payload modified
		if err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, beforeIdent, afterIdent, []byte("tampered\n"), false, false, true); err == nil {
			t.Fatal("expected error when outside target payload was modified")
		}

		// Negative: target inode changed
		badIdent := DarwinTargetIdentity{Dev: 100, Ino: 999}
		if err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, beforeIdent, badIdent, payload, false, false, true); err == nil {
			t.Fatal("expected error when outside target inode changed")
		}

		// Negative: run root not reclaimed
		if err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, beforeIdent, afterIdent, payload, true, false, true); err == nil {
			t.Fatal("expected error when run root was not reclaimed")
		}

		// Negative: symlink not reclaimed
		if err := verifyDarwinFixtureLinkEscape(escapeOutput, plan, beforeIdent, afterIdent, payload, false, true, true); err == nil {
			t.Fatal("expected error when symlink was not reclaimed")
		}
	})

	t.Run("EscapeRecordParserStrictness", func(t *testing.T) {
		valid := "path=/safe/a\nwrite=eperm\nremove=eperm\nread=eperm\n"
		recs, err := parseDarwinEscapeRecords(valid)
		if err != nil || len(recs) != 1 || recs["/safe/a"].Read != "eperm" {
			t.Fatalf("unexpected failure parsing valid records: %v", err)
		}

		// Missing read outcome
		if _, err := parseDarwinEscapeRecords("path=/a\nwrite=eperm\nremove=eperm\n"); err == nil {
			t.Fatal("expected error on missing read field")
		}

		// Missing write outcome
		if _, err := parseDarwinEscapeRecords("path=/a\nremove=eperm\nread=eperm\n"); err == nil {
			t.Fatal("expected error on missing write field")
		}

		// Missing remove outcome
		if _, err := parseDarwinEscapeRecords("path=/a\nwrite=eperm\nread=eperm\n"); err == nil {
			t.Fatal("expected error on missing remove field")
		}

		// Duplicate write field
		if _, err := parseDarwinEscapeRecords("path=/a\nwrite=nil\nremove=eperm\nread=eperm\nwrite=eperm\n"); err == nil {
			t.Fatal("expected error on duplicate write field")
		}

		// Duplicate read field
		if _, err := parseDarwinEscapeRecords("path=/a\nwrite=eperm\nremove=eperm\nread=nil\nread=eperm\n"); err == nil {
			t.Fatal("expected error on duplicate read field")
		}

		// Unknown field
		if _, err := parseDarwinEscapeRecords("path=/a\nwrite=eperm\nremove=eperm\nread=eperm\nunknown=123\n"); err == nil {
			t.Fatal("expected error on unknown field")
		}

		// Duplicate record for same path
		dupPath := "path=/a\nwrite=eperm\nremove=eperm\nread=eperm\npath=/a\nwrite=eperm\nremove=eperm\nread=eperm\n"
		if _, err := parseDarwinEscapeRecords(dupPath); err == nil {
			t.Fatal("expected error on duplicate record for same path")
		}

		// Invalid outcome value
		if _, err := parseDarwinEscapeRecords("path=/a\nwrite=invalid_val\nremove=eperm\nread=eperm\n"); err == nil {
			t.Fatal("expected error on invalid outcome value")
		}

		// Field before path
		if _, err := parseDarwinEscapeRecords("write=eperm\npath=/a\nremove=eperm\nread=eperm\n"); err == nil {
			t.Fatal("expected error on field before path")
		}
	})

	t.Run("WorkerProbeSeamPlatformScope", func(t *testing.T) {
		readFileCalled := false
		writeFileCalled := false
		removeCalled := false

		mockSeam := workerEscapeProberSeam{
			WriteFile: func(name string, data []byte, perm os.FileMode) error {
				writeFileCalled = true
				return nil
			},
			Remove: func(name string) error {
				removeCalled = true
				return nil
			},
			ReadFile: func(name string) ([]byte, error) {
				readFileCalled = true
				return []byte("data"), nil
			},
		}

		// On Windows: ReadFile is NEVER called, and format has no read= field (restores 6041)
		mockSeam.GOOS = "windows"
		readFileCalled = false
		writeFileCalled = false
		removeCalled = false
		winOut := probeAndFormatWorkerEscape("/fake/target", mockSeam)
		if !writeFileCalled || !removeCalled {
			t.Fatal("expected write and remove called on windows")
		}
		if readFileCalled {
			t.Fatal("windows probe illegally called ReadFile; PR pollution detected")
		}
		if strings.Contains(winOut, "read=") {
			t.Fatalf("windows probe output illegally contains read= field: %q", winOut)
		}
		wantWin := "path=/fake/target\nwrite=nil\nremove=nil\n"
		if winOut != wantWin {
			t.Fatalf("windows probe output mismatch: got %q, want %q", winOut, wantWin)
		}

		// On Darwin: ReadFile IS called, and format includes read= field
		mockSeam.GOOS = "darwin"
		readFileCalled = false
		darwinOut := probeAndFormatWorkerEscape("/fake/target", mockSeam)
		if !readFileCalled {
			t.Fatal("darwin probe must invoke ReadFile for read denial verification")
		}
		if !strings.Contains(darwinOut, "read=") {
			t.Fatalf("darwin probe output missing read= field: %q", darwinOut)
		}
		wantDarwin := "path=/fake/target\nwrite=nil\nremove=nil\nread=nil\n"
		if darwinOut != wantDarwin {
			t.Fatalf("darwin probe output mismatch: got %q, want %q", darwinOut, wantDarwin)
		}
	})

	t.Run("LaunchAllowedReadsResolution", func(t *testing.T) {
		clearDarwinRegistries()
		defer clearDarwinRegistries()

		restoreInspector := setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 10, 20, true, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil
		})
		defer restoreInspector()

		rec := RunRecord{
			RunID:       "run1234567890abcdef1234567890ab",
			Root:        "/private/tmp/agentpulse-runs/run1234567890abcdef1234567890ab",
			Nonce:       "nonce1234567890abcdef1234567890ab",
			ExeSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			SentinelDev: 100,
			SentinelIno: 200,
		}
		selfExe := "/usr/bin/agentpulse"
		homes := []string{"/Users/alice", "/Volumes/custom-home/bob"}

		// Baseline: without synthetic probe, allowed reads contains ONLY selfExe
		baselineReads, err := resolveDarwinAllowedReads(rec, selfExe, homes)
		if err != nil {
			t.Fatalf("unexpected error resolving baseline reads: %v", err)
		}
		if len(baselineReads) != 1 || baselineReads[0] != selfExe {
			t.Fatalf("expected only selfExe in baseline, got %v", baselineReads)
		}

		// Arbitrary caller sentinel (unverified) fails closed and is NOT added
		recWithCaller := rec
		recWithCaller.Sentinel = "/private/tmp/not-created-by-this-run.txt"
		callerReads, err := resolveDarwinAllowedReads(recWithCaller, selfExe, homes)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(callerReads) != 1 || callerReads[0] != selfExe {
			t.Fatalf("unverified caller sentinel illegally entered allowed reads: %v", callerReads)
		}

		// Custom HOME sentinel fails closed and is NOT added
		recWithCustomHome := rec
		recWithCustomHome.Sentinel = "/Volumes/custom-home/bob/secret.txt"
		customHomeReads, err := resolveDarwinAllowedReads(recWithCustomHome, selfExe, homes)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(customHomeReads) != 1 {
			t.Fatalf("custom home sentinel illegally entered allowed reads: %v", customHomeReads)
		}

		// Verified this-run synthetic probe enters allowed reads
		safeProbe := "/private/tmp/agentpulse-sentinels/sentinel-bound1"
		authorizeProbeFromExclusiveFD(t, rec.RunID, rec.Root, rec.Nonce, safeProbe, 100, 200)
		recWithSynthetic := rec
		recWithSynthetic.Sentinel = safeProbe
		synthReads, err := resolveDarwinAllowedReads(recWithSynthetic, selfExe, homes)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(synthReads) != 2 || synthReads[1] != safeProbe {
			t.Fatalf("expected synthetic probe in allowed reads, got %v", synthReads)
		}

		// Self executable inside protected home is rejected
		badExe := "/Users/alice/bin/agentpulse"
		if _, err := resolveDarwinAllowedReads(rec, badExe, homes); err == nil {
			t.Fatal("expected error when self executable is inside protected home")
		}
	})

	t.Run("ComposedResolveProfileContract", func(t *testing.T) {
		clearDarwinRegistries()
		defer clearDarwinRegistries()

		restoreInspector := setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 10, 20, true, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil
		})
		defer restoreInspector()

		rec := RunRecord{
			RunID:       "run1234567890abcdef1234567890ab",
			Root:        "/private/tmp/agentpulse-runs/run1234567890abcdef1234567890ab",
			Nonce:       "nonce1234567890abcdef1234567890ab",
			ExeSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			SentinelDev: 100,
			SentinelIno: 200,
		}
		selfExe := "/usr/bin/agentpulse"
		homes := []string{"/Users/alice"}

		safeProbe := "/private/tmp/agentpulse-sentinels/sentinel-composed1"
		authorizeProbeFromExclusiveFD(t, rec.RunID, rec.Root, rec.Nonce, safeProbe, 100, 200)
		rec.Sentinel = safeProbe

		// 1. Resolve allowed reads contains both selfExe and safeProbe
		allowedReads, err := resolveDarwinAllowedReads(rec, selfExe, homes)
		if err != nil {
			t.Fatalf("resolveDarwinAllowedReads failed: %v", err)
		}
		if len(allowedReads) != 2 || allowedReads[0] != selfExe || allowedReads[1] != safeProbe {
			t.Fatalf("expected [selfExe safeProbe], got %v", allowedReads)
		}

		// 2. Pure composition: DarwinSeatbeltProfile with allowedReads must succeed
		prof, err := DarwinSeatbeltProfile(rec.Root, allowedReads...)
		if err != nil {
			t.Fatalf("DarwinSeatbeltProfile failed on composed validated image + owned probe: %v", err)
		}
		wantExeLit := fmt.Sprintf("(allow file-read* (literal %q))", selfExe)
		if !strings.Contains(prof, wantExeLit) {
			t.Errorf("seatbelt profile missing selfExe literal %q", wantExeLit)
		}
		wantProbeLit := fmt.Sprintf("(allow file-read* (literal %q))", safeProbe)
		if !strings.Contains(prof, wantProbeLit) {
			t.Errorf("seatbelt profile missing safeProbe literal %q", wantProbeLit)
		}

		// 3. Arbitrary untrusted path outside runRoot MUST be rejected
		untrusted := "/private/tmp/agentpulse-sentinels/untrusted-probe.txt"
		if _, err := DarwinSeatbeltProfile(rec.Root, untrusted); err == nil {
			t.Errorf("expected DarwinSeatbeltProfile to reject arbitrary untrusted path %q, got nil", untrusted)
		}
	})

	t.Run("SyntheticOwnershipIdentityBinding", func(t *testing.T) {
		clearDarwinRegistries()
		defer clearDarwinRegistries()

		restoreInspector := setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 10, 20, true, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil
		})
		defer restoreInspector()

		rec := RunRecord{
			RunID:       "run1234567890abcdef1234567890ab",
			Root:        "/private/tmp/agentpulse-runs/run1234567890abcdef1234567890ab",
			Nonce:       "nonce1234567890abcdef1234567890ab",
			ExeSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			SentinelDev: 100,
			SentinelIno: 200,
		}
		selfExe := "/usr/bin/agentpulse"
		homes := []string{"/Users/alice"}

		safeProbe := "/private/tmp/agentpulse-sentinels/sentinel-bindtest1"
		authorizeProbeFromExclusiveFD(t, rec.RunID, rec.Root, rec.Nonce, safeProbe, 100, 200)
		rec.Sentinel = safeProbe

		// Positive verification: exact match succeeds
		reads, err := resolveDarwinAllowedReads(rec, selfExe, homes)
		if err != nil || len(reads) != 2 || reads[1] != safeProbe {
			t.Fatalf("expected positive match to return [selfExe safeProbe], got %v (err=%v)", reads, err)
		}

		// Negative 1: Empty RunID in record must NOT act as wildcard (must fail closed)
		recNoRunID := rec
		recNoRunID.RunID = ""
		_, err = resolveDarwinAllowedReads(recNoRunID, selfExe, homes)
		if err == nil {
			t.Fatal("empty RunID must fail closed before an image proof is minted")
		}

		// Negative 2: Empty Nonce in record must NOT grant permission or mint an image proof
		recNoNonce := rec
		recNoNonce.Nonce = ""
		_, err = resolveDarwinAllowedReads(recNoNonce, selfExe, homes)
		if err == nil {
			t.Fatal("empty Nonce must fail closed before an image proof is minted")
		}

		// Negative 3: Mismatched Nonce must fail closed
		recBadNonce := rec
		recBadNonce.Nonce = "different-nonce"
		readsBadNonce, err := resolveDarwinAllowedReads(recBadNonce, selfExe, homes)
		if err == nil && len(readsBadNonce) > 1 {
			t.Fatalf("mismatched Nonce granted sentinel read permission: %v", readsBadNonce)
		}

		// Negative 4: Dev mismatch must fail closed
		recBadDev := rec
		recBadDev.SentinelDev = 999
		readsBadDev, err := resolveDarwinAllowedReads(recBadDev, selfExe, homes)
		if err == nil && len(readsBadDev) > 1 {
			t.Fatalf("mismatched Dev granted sentinel read permission: %v", readsBadDev)
		}

		// Negative 5: Ino mismatch must fail closed
		recBadIno := rec
		recBadIno.SentinelIno = 999
		readsBadIno, err := resolveDarwinAllowedReads(recBadIno, selfExe, homes)
		if err == nil && len(readsBadIno) > 1 {
			t.Fatalf("mismatched Ino granted sentinel read permission: %v", readsBadIno)
		}

		// Negative 6: Zero Dev or Ino must fail closed
		recZeroDev := rec
		recZeroDev.SentinelDev = 0
		readsZeroDev, err := resolveDarwinAllowedReads(recZeroDev, selfExe, homes)
		if err == nil && len(readsZeroDev) > 1 {
			t.Fatalf("zero Dev granted sentinel read permission: %v", readsZeroDev)
		}

		// Negative 7: Stale proof for same pathname from another run must NOT grant permission
		recOtherRun := rec
		recOtherRun.RunID = "run-other-99999999999999999999"
		recOtherRun.Nonce = "nonce-other-8888888888888888888"
		readsOtherRun, err := resolveDarwinAllowedReads(recOtherRun, selfExe, homes)
		if err == nil && len(readsOtherRun) > 1 {
			t.Fatalf("stale proof granted permission to different run: %v", readsOtherRun)
		}

		// Negative 8: Empty Root in record must NOT act as wildcard (must fail closed)
		recNoRoot := rec
		recNoRoot.Root = ""
		if _, err := resolveDarwinAllowedReads(recNoRoot, selfExe, homes); err == nil {
			t.Fatal("empty Root in record must fail closed")
		}

		// Negative 9: Target executable image must not be blindly minted; unverified image must fail closed
		recWrongExe := rec
		recWrongExe.ExeSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		if _, err := resolveDarwinAllowedReads(recWrongExe, selfExe, homes); err == nil {
			t.Fatal("expected resolveDarwinAllowedReads to fail closed on unverified/mismatched image hash")
		}

		// Negative 10: Mismatched Root between record and proof must fail closed
		recBadRoot := rec
		recBadRoot.Root = "/private/tmp/agentpulse-runs/different-root-directory"
		readsBadRoot, err := resolveDarwinAllowedReads(recBadRoot, selfExe, homes)
		if err == nil && len(readsBadRoot) > 1 {
			t.Fatalf("mismatched Root granted sentinel read permission: %v", readsBadRoot)
		}

		// Negative 11: Sentinel replaced after exclusive creation (mismatched dev/ino)
		swappedSentinel := "/private/tmp/agentpulse-sentinels/sentinel-swapped1"
		pendingFromExclusiveFD(t, swappedSentinel, 500, 600)
		if err := bindDarwinSyntheticProbe(rec.RunID, rec.Root, rec.Nonce, swappedSentinel, 500, 999); err == nil {
			t.Fatal("expected bindDarwinSyntheticProbe to reject replaced sentinel identity")
		}

		// Positive & exclusive provenance transition: pending creation -> one-run bind
		pendingSentinel := "/private/tmp/agentpulse-sentinels/sentinel-pending1"
		pendingFromExclusiveFD(t, pendingSentinel, 300, 400)
		recPending := RunRecord{
			RunID:       "run-fresh-11111111111111111111",
			Root:        "/private/tmp/agentpulse-runs/run-fresh-11111111111111111111",
			Nonce:       "nonce-fresh-2222222222222222222",
			ExeSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Sentinel:    pendingSentinel,
			SentinelDev: 300,
			SentinelIno: 400,
		}
		if err := bindDarwinSyntheticProbe(recPending.RunID, recPending.Root, recPending.Nonce, recPending.Sentinel, recPending.SentinelDev, recPending.SentinelIno); err != nil {
			t.Fatalf("bindDarwinSyntheticProbe failed: %v", err)
		}
		boundReads, err := resolveDarwinAllowedReads(recPending, selfExe, homes)
		if err != nil || len(boundReads) != 2 || boundReads[1] != pendingSentinel {
			t.Fatalf("expected bound pending sentinel in allowed reads, got %v (err=%v)", boundReads, err)
		}
		// Exclusive consumption: second run cannot bind the already-consumed pending sentinel
		recSecondRun := recPending
		recSecondRun.RunID = "run-fresh-second-run-33333333333"
		recSecondRun.Nonce = "nonce-fresh-second-run-444444444"
		if err := bindDarwinSyntheticProbe(recSecondRun.RunID, recSecondRun.Root, recSecondRun.Nonce, recSecondRun.Sentinel, recSecondRun.SentinelDev, recSecondRun.SentinelIno); err != nil {
			t.Fatalf("unexpected error on second bind attempt: %v", err)
		}
		secondReads, err := resolveDarwinAllowedReads(recSecondRun, selfExe, homes)
		if err != nil || len(secondReads) != 1 || secondReads[0] != selfExe {
			t.Fatalf("second run illegally obtained read permission for consumed sentinel: %v", secondReads)
		}
	})

	t.Run("ImageSourceProofInspectionAndBindingContract", func(t *testing.T) {
		clearDarwinRegistries()
		defer clearDarwinRegistries()

		rec := RunRecord{
			RunID:     "run-img-1234567890abcdef12345678",
			Root:      "/private/tmp/agentpulse-runs/run-img-1234567890abcdef12345678",
			Nonce:     "nonce-img-1234567890abcdef123456",
			ExeSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		}
		selfExe := "/usr/bin/agentpulse"
		homes := []string{"/Users/alice"}

		// Positive verification: valid regular file, dev/ino, canonical path, and matching hash
		restore := setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 10, 20, true, rec.ExeSHA256, nil
		})
		defer restore()

		reads, err := resolveDarwinAllowedReads(rec, selfExe, homes)
		if err != nil {
			t.Fatalf("expected positive resolveDarwinAllowedReads to succeed: %v", err)
		}
		if len(reads) != 1 || reads[0] != selfExe {
			t.Fatalf("unexpected allowed reads: %v", reads)
		}
		if !isVerifiedDarwinImage(selfExe, rec.Root) {
			t.Fatalf("expected selfExe to be registered as verified Darwin image for root %s", rec.Root)
		}

		// Boundary failure 1: OS lookup / inspect error fails closed
		setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return "", 0, 0, false, "", fmt.Errorf("file not found")
		})
		if _, err := resolveDarwinAllowedReads(rec, selfExe, homes); err == nil {
			t.Fatal("expected inspect error to fail closed")
		}

		// Boundary failure 2: non-regular file (e.g. directory / FIFO) fails closed
		setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 10, 20, false, rec.ExeSHA256, nil
		})
		if _, err := resolveDarwinAllowedReads(rec, selfExe, homes); err == nil {
			t.Fatal("expected non-regular file to fail closed")
		}

		// Boundary failure 3: zero device or inode fails closed
		setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 0, 0, true, rec.ExeSHA256, nil
		})
		if _, err := resolveDarwinAllowedReads(rec, selfExe, homes); err == nil {
			t.Fatal("expected zero dev/ino to fail closed")
		}

		// Boundary failure 4: canonical path inside protected home fails closed
		setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return "/Users/alice/bin/agentpulse", 10, 20, true, rec.ExeSHA256, nil
		})
		if _, err := resolveDarwinAllowedReads(rec, selfExe, homes); err == nil {
			t.Fatal("expected canonical path inside protected home to fail closed")
		}

		// Boundary failure 5: hash mismatch vs rec.ExeSHA256 fails closed
		setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 10, 20, true, "badhash111111111111111111111111111111111111111111111111111111111", nil
		})
		if _, err := resolveDarwinAllowedReads(rec, selfExe, homes); err == nil {
			t.Fatal("expected hash mismatch to fail closed")
		}

		// Boundary failure 6: nil inspector callback fails closed
		setDarwinImageInspector(nil)
		if _, err := resolveDarwinAllowedReads(rec, selfExe, homes); err == nil {
			t.Fatal("expected nil inspector to fail closed")
		}
	})

	t.Run("AtomicTwoConsumerProbeRaceContract", func(t *testing.T) {
		clearDarwinRegistries()
		defer clearDarwinRegistries()

		restore := setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
			return path.Clean(exePath), 10, 20, true, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil
		})
		defer restore()

		sentPath := "/private/tmp/agentpulse-sentinels/sentinel-race1"
		pendingFromExclusiveFD(t, sentPath, 111, 222)

		recA := RunRecord{
			RunID:       "run-race-aaaa11112222333344445555",
			Root:        "/private/tmp/agentpulse-runs/run-race-aaaa11112222333344445555",
			Nonce:       "nonce-race-aaaa111122223333444455",
			ExeSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Sentinel:    sentPath,
			SentinelDev: 111,
			SentinelIno: 222,
		}
		recB := RunRecord{
			RunID:       "run-race-bbbb11112222333344445555",
			Root:        "/private/tmp/agentpulse-runs/run-race-bbbb11112222333344445555",
			Nonce:       "nonce-race-bbbb111122223333444455",
			ExeSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Sentinel:    sentPath,
			SentinelDev: 111,
			SentinelIno: 222,
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = bindDarwinSyntheticProbe(recA.RunID, recA.Root, recA.Nonce, recA.Sentinel, recA.SentinelDev, recA.SentinelIno)
		}()
		go func() {
			defer wg.Done()
			_ = bindDarwinSyntheticProbe(recB.RunID, recB.Root, recB.Nonce, recB.Sentinel, recB.SentinelDev, recB.SentinelIno)
		}()
		wg.Wait()

		readsA, errResA := resolveDarwinAllowedReads(recA, "/usr/bin/agentpulse", nil)
		readsB, errResB := resolveDarwinAllowedReads(recB, "/usr/bin/agentpulse", nil)
		if errResA != nil || errResB != nil {
			t.Fatalf("unexpected resolver error: errResA=%v errResB=%v", errResA, errResB)
		}

		winnerA := len(readsA) == 2
		winnerB := len(readsB) == 2

		// Exactly one winner must receive the probe grant!
		if winnerA == winnerB {
			t.Fatalf("race contract violation: expected exactly one winner, got winnerA=%v winnerB=%v", winnerA, winnerB)
		}

		// Stale proof from consumed probe cannot be used by a third run
		recC := recA
		recC.RunID = "run-race-cccc11112222333344445555"
		recC.Nonce = "nonce-race-cccc111122223333444455"
		readsC, errC := resolveDarwinAllowedReads(recC, "/usr/bin/agentpulse", nil)
		if errC != nil || len(readsC) != 1 {
			t.Fatalf("third run illegally obtained read permission for consumed sentinel: %v", readsC)
		}

		// Permanently burned on failed binding validation
		burnedSent := "/private/tmp/agentpulse-sentinels/sentinel-burned1"
		pendingFromExclusiveFD(t, burnedSent, 333, 444)
		// Attempt bind with mismatched Dev -> must fail
		if err := bindDarwinSyntheticProbe(recA.RunID, recA.Root, recA.Nonce, burnedSent, 999, 444); err == nil {
			t.Fatal("expected mismatched Dev to fail")
		}
		// Subsequent attempt even with correct Dev must fail because probe was consumed/burned
		if err := bindDarwinSyntheticProbe(recA.RunID, recA.Root, recA.Nonce, burnedSent, 333, 444); err != nil {
			t.Fatalf("unexpected error on already consumed probe: %v", err)
		}
		readsBurned, _ := resolveDarwinAllowedReads(RunRecord{
			RunID: recA.RunID, Root: recA.Root, Nonce: recA.Nonce,
			ExeSHA256: recA.ExeSHA256, Sentinel: burnedSent, SentinelDev: 333, SentinelIno: 444,
		}, "/usr/bin/agentpulse", nil)
		if len(readsBurned) != 1 {
			t.Fatalf("burned probe illegally granted read permission: %v", readsBurned)
		}
	})
}

func TestDarwinConfirmPipeContract(t *testing.T) {
	t.Run("ParsePositive", func(t *testing.T) {
		ok, reason, err := parseDarwinConfirmMessage("OK\n")
		if !ok || reason != "OK" || err != nil {
			t.Fatalf("parseDarwinConfirmMessage(OK) failed: ok=%v reason=%s err=%v", ok, reason, err)
		}
	})

	t.Run("ParseNegative", func(t *testing.T) {
		ok, reason, err := parseDarwinConfirmMessage("FAIL root_identity\n")
		if ok || reason != "root_identity" || err == nil {
			t.Fatalf("parseDarwinConfirmMessage(FAIL) unexpected: ok=%v reason=%s err=%v", ok, reason, err)
		}
	})

	t.Run("ParseTruncated", func(t *testing.T) {
		ok, reason, err := parseDarwinConfirmMessage("")
		if ok || !strings.Contains(reason, "closed confirm pipe") || err == nil {
			t.Fatalf("parseDarwinConfirmMessage('') unexpected: ok=%v reason=%s err=%v", ok, reason, err)
		}
	})

	t.Run("ReexecEPIPE", func(t *testing.T) {
		execCalls := 0
		seam := darwinConfirmExecSeam{
			Write: func(fd int, b []byte) (int, error) {
				return 0, syscall.EPIPE
			},
			Close: func(fd int) error {
				return nil
			},
			Exec: func(argv0 string, argv []string, env []string) error {
				execCalls++
				return nil
			},
		}
		err := darwinConfirmAndExec(3, []string{"/bin/echo", "hi"}, []string{}, seam)
		if err == nil {
			t.Fatal("expected error on EPIPE write failure, got nil")
		}
		if !errors.Is(err, ErrRefused) || !errors.Is(err, syscall.EPIPE) {
			t.Fatalf("expected ErrRefused and EPIPE, got %v", err)
		}
		if execCalls != 0 {
			t.Fatalf("exec called %d times on write failure; expected 0", execCalls)
		}
	})

	t.Run("ReexecShortWrite", func(t *testing.T) {
		execCalls := 0
		seam := darwinConfirmExecSeam{
			Write: func(fd int, b []byte) (int, error) {
				return 1, nil // short write (1 < 3)
			},
			Close: func(fd int) error {
				return nil
			},
			Exec: func(argv0 string, argv []string, env []string) error {
				execCalls++
				return nil
			},
		}
		err := darwinConfirmAndExec(3, []string{"/bin/echo", "hi"}, []string{}, seam)
		if err == nil {
			t.Fatal("expected error on short write, got nil")
		}
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("expected ErrRefused on short write, got %v", err)
		}
		if execCalls != 0 {
			t.Fatalf("exec called %d times on short write; expected 0", execCalls)
		}
	})

	t.Run("ReexecCloseError", func(t *testing.T) {
		execCalls := 0
		seam := darwinConfirmExecSeam{
			Write: func(fd int, b []byte) (int, error) {
				return len(b), nil
			},
			Close: func(fd int) error {
				return syscall.EBADF
			},
			Exec: func(argv0 string, argv []string, env []string) error {
				execCalls++
				return nil
			},
		}
		err := darwinConfirmAndExec(3, []string{"/bin/echo", "hi"}, []string{}, seam)
		if err == nil {
			t.Fatal("expected error on close failure, got nil")
		}
		if !errors.Is(err, ErrRefused) || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("expected ErrRefused and EBADF on close failure, got %v", err)
		}
		if execCalls != 0 {
			t.Fatalf("exec called %d times on close failure; expected 0", execCalls)
		}
	})

	t.Run("ReexecSuccess", func(t *testing.T) {
		execCalls := 0
		var gotArgv0 string
		var gotArgv []string
		seam := darwinConfirmExecSeam{
			Write: func(fd int, b []byte) (int, error) {
				return len(b), nil
			},
			Close: func(fd int) error {
				return nil
			},
			Exec: func(argv0 string, argv []string, env []string) error {
				execCalls++
				gotArgv0 = argv0
				gotArgv = argv
				return nil
			},
		}
		err := darwinConfirmAndExec(3, []string{"/bin/echo", "hello"}, []string{"FOO=bar"}, seam)
		if err != nil {
			t.Fatalf("unexpected error on complete success: %v", err)
		}
		if execCalls != 1 {
			t.Fatalf("expected exec called exactly once, got %d", execCalls)
		}
		if gotArgv0 != "/bin/echo" || len(gotArgv) != 2 || gotArgv[1] != "hello" {
			t.Fatalf("unexpected exec argv: %s %v", gotArgv0, gotArgv)
		}
	})

	t.Run("ReexecEmptyUser", func(t *testing.T) {
		execCalls := 0
		seam := darwinConfirmExecSeam{
			Write: func(fd int, b []byte) (int, error) {
				return len(b), nil
			},
			Close: func(fd int) error {
				return nil
			},
			Exec: func(argv0 string, argv []string, env []string) error {
				execCalls++
				return nil
			},
		}
		err := darwinConfirmAndExec(3, []string{}, []string{}, seam)
		if err == nil {
			t.Fatal("expected error on empty user command, got nil")
		}
		if execCalls != 0 {
			t.Fatalf("exec called on empty command: %d", execCalls)
		}
	})
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

	t.Run("MarkerValidation", func(t *testing.T) {
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
	})

	t.Run("RegistryRemovalSuccess", func(t *testing.T) {
		removeCalls := 0
		removeFn := func(path string) error {
			removeCalls++
			if path != "/private/tmp/registry/run1.json" {
				t.Fatalf("unexpected remove path: %s", path)
			}
			return nil
		}
		out, err := handleDarwinRegistryRemoval("/private/tmp/registry/run1.json", "/private/tmp/runs/run1", removeFn)
		if err != nil {
			t.Fatalf("unexpected error on successful removal: %v", err)
		}
		if out.Result != "deleted" {
			t.Fatalf("expected Result 'deleted', got %q", out.Result)
		}
		if len(out.Targets) != 1 || out.Targets[0] != "/private/tmp/runs/run1" {
			t.Fatalf("unexpected targets: %v", out.Targets)
		}
		if removeCalls != 1 {
			t.Fatalf("expected 1 remove call, got %d", removeCalls)
		}
	})

	t.Run("RegistryRemovalNotExist", func(t *testing.T) {
		removeFn := func(path string) error {
			return os.ErrNotExist
		}
		out, err := handleDarwinRegistryRemoval("/private/tmp/registry/run1.json", "/private/tmp/runs/run1", removeFn)
		if err != nil {
			t.Fatalf("unexpected error when registry already not exist: %v", err)
		}
		if out.Result != "deleted" {
			t.Fatalf("expected Result 'deleted', got %q", out.Result)
		}
	})

	t.Run("RegistryRemovalEmpty", func(t *testing.T) {
		out, err := handleDarwinRegistryRemoval("", "/private/tmp/runs/run1", nil)
		if err != nil {
			t.Fatalf("unexpected error for empty registry path: %v", err)
		}
		if out.Result != "deleted" {
			t.Fatalf("expected Result 'deleted', got %q", out.Result)
		}
	})

	t.Run("RegistryRemovalPermissionDeniedPartialState", func(t *testing.T) {
		removeFn := func(path string) error {
			return os.ErrPermission
		}
		out, err := handleDarwinRegistryRemoval("/private/tmp/registry/run1.json", "/private/tmp/runs/run1", removeFn)
		if err == nil {
			t.Fatal("expected error on permission denied registry removal, got nil")
		}
		if !errors.Is(err, ErrCleanupBlocked) || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("expected wrapped ErrCleanupBlocked and os.ErrPermission, got %v", err)
		}
		if out.Result != "partial" {
			t.Fatalf("expected honest partial result 'partial', got %q", out.Result)
		}
		if !strings.Contains(out.Reason, "root removed, registry retained") {
			t.Fatalf("expected reason stating root removed, registry retained, got %q", out.Reason)
		}
		if len(out.Targets) != 1 || out.Targets[0] != "/private/tmp/runs/run1" {
			t.Fatalf("expected root retained in targets list: %v", out.Targets)
		}
	})
}

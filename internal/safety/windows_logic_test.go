package safety

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcceptCreatedProfileRejectsCollisionAndOtherErrors(t *testing.T) {
	if err := acceptCreatedProfile(0); err != nil {
		t.Fatal(err)
	}
	err := acceptCreatedProfile(0x800700B7)
	if err == nil || !errors.Is(err, err) {
		t.Fatal(err)
	}
	if !errors.Is(err, errors.New("x")) && err.Error() == "" {
		t.Fatal(err)
	}
	if !containsAll(err.Error(), "already exists", "refusing to adopt") {
		t.Fatalf("collision: %v", err)
	}
	err = acceptCreatedProfile(0x80070005)
	if err == nil || !containsAll(err.Error(), "HRESULT", "0x80070005") {
		t.Fatalf("other hr: %v", err)
	}
}

func TestSentinelAllowedOnlyInsideThisFixture(t *testing.T) {
	root := filepath.Join(string(filepath.Separator)+"tmp", "agentpulse-fixture-abc")
	sentinel := filepath.Join(root, "fake-home", ".ssh", "id_ed25519")
	if err := sentinelAllowed(sentinel, root); err != nil {
		t.Fatal(err)
	}
	if err := sentinelAllowed(sentinel, filepath.Join(root, "runs")); err == nil {
		t.Fatal("runs subdirectory must not authorize a protected sentinel")
	}
	if err := sentinelAllowed(filepath.Join(string(filepath.Separator)+"home", "user", ".ssh", "id"), root); err == nil {
		t.Fatal("real protected path was accepted")
	}
	if err := sentinelAllowed(sentinel, filepath.Join(string(filepath.Separator)+"tmp", "not-our-fixture")); err == nil {
		t.Fatal("unprefixed directory was accepted")
	}
	plain := filepath.Join(root, "sentinel-plain")
	if err := sentinelAllowed(plain, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRequireAccessDeniedUsesEachOperation(t *testing.T) {
	if err := requireAccessDenied(os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	if err := requireAccessDenied(nil); err == nil || err.Error() != "sentinel_not_denied" {
		t.Fatalf("nil: %v", err)
	}
	if err := requireAccessDenied(os.ErrNotExist); err == nil || err.Error() != "sentinel_enoent" {
		t.Fatalf("enoent: %v", err)
	}
	if err := requireAccessDenied(errors.New("sharing violation")); err == nil || err.Error() != "sentinel_not_denied" {
		t.Fatalf("other: %v", err)
	}
}

func TestExitFromWaitPreservesNonZeroStatus(t *testing.T) {
	err := exitFromWait(23, nil)
	code, ok := exitCodeOf(err)
	if !ok || code != 23 {
		t.Fatalf("code=%d ok=%v err=%v", code, ok, err)
	}
	if err := exitFromWait(0, nil); err != nil {
		t.Fatal(err)
	}
	code, ok = exitCodeOf(exitFromWait(0, errors.New("wait failed")))
	if ok || code != 0 {
		t.Fatalf("wait failure looked like an exit: %d %v", code, ok)
	}
}

func TestFinalizeDeleteDoesNotClaimSuccess(t *testing.T) {
	out, err := finalizeDelete(true, nil)
	if err != nil || out.Result != "deleted" {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = finalizeDelete(false, nil)
	if err == nil || out.Result != "BLOCKED" {
		t.Fatalf("root remained: %+v %v", out, err)
	}
	out, err = finalizeDelete(true, errors.New("profile busy"))
	if err == nil || out.Result != "BLOCKED" || !containsAll(out.Reason, "profile delete failed") {
		t.Fatalf("profile: %+v %v", out, err)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, part string) bool {
	return len(part) == 0 || (len(s) >= len(part) && (s == part || len(s) > 0 && (indexOf(s, part) >= 0)))
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}

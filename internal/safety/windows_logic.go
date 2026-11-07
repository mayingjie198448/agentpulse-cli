package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// acceptCreatedProfile allows only a brand-new AppContainer profile (S_OK).
// ERROR_ALREADY_EXISTS and every other HRESULT refuse. Callers must not derive
// or delete a profile they did not just create.
func acceptCreatedProfile(hr uint32) error {
	if hr == 0 {
		return nil
	}
	if hr == 0x800700B7 {
		return fmt.Errorf("BLOCKED: AppContainer profile already exists; refusing to adopt or delete it")
	}
	return fmt.Errorf("BLOCKED: CreateAppContainerProfile HRESULT 0x%08X", hr)
}

// sentinelAllowed permits a protected path component only inside a fixture
// directory this run created under Temp, named agentpulse-fixture-*.
// It does not allow an arbitrary BaseDir or the real user profile.
func sentinelAllowed(sentinel, ownedRoot string) error {
	if !protectedPathComponent(sentinel) {
		return nil
	}
	if strings.TrimSpace(ownedRoot) == "" || protectedPathComponent(ownedRoot) {
		return fmt.Errorf("%w: refusing to use a protected user path as the sentinel", ErrSentinelUnusable)
	}
	base := filepath.Base(ownedRoot)
	if !strings.HasPrefix(strings.ToLower(base), "agentpulse-fixture-") {
		return fmt.Errorf("%w: protected sentinel is not inside this run's fixture directory", ErrSentinelUnusable)
	}
	if !withinPath(ownedRoot, sentinel) {
		return fmt.Errorf("%w: protected sentinel is outside the owned fixture root", ErrSentinelUnusable)
	}
	return nil
}

func protectedPathComponent(path string) bool {
	for _, part := range splitPath(path) {
		switch strings.ToLower(part) {
		case ".ssh", ".config", "documents", "desktop", "downloads", "onedrive",
			"cloudstorage", "keychains", "cookies", "dpapi":
			return true
		}
	}
	return false
}

// requireAccessDenied classifies one filesystem operation. ENOENT is not denial.
func requireAccessDenied(err error) error {
	if err == nil {
		return fmt.Errorf("sentinel_not_denied")
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("sentinel_enoent")
	}
	if os.IsPermission(err) {
		return nil
	}
	return fmt.Errorf("sentinel_not_denied")
}

// codedExit preserves a finished process status when Wait returns a nil error.
type codedExit struct{ code int }

func (e *codedExit) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *codedExit) ExitCode() int { return e.code }

// exitFromWait keeps a non-zero process status when the wait itself succeeded.
func exitFromWait(code int, waitErr error) error {
	if waitErr != nil {
		return waitErr
	}
	if code != 0 {
		return &codedExit{code: code}
	}
	return nil
}

// finalizeDelete refuses to report success when the root remains or profile
// deletion failed. The registry is kept on failure.
func finalizeDelete(rootGone bool, profileErr error) (CleanupOutcome, error) {
	if !rootGone {
		return CleanupOutcome{Result: "BLOCKED", Reason: "root still present or replaced after delete"}, fmt.Errorf("%w: root still present or replaced after delete", ErrCleanupBlocked)
	}
	if profileErr != nil {
		return CleanupOutcome{Result: "BLOCKED", Reason: "profile delete failed: " + profileErr.Error()}, fmt.Errorf("%w: profile delete failed: %v", ErrCleanupBlocked, profileErr)
	}
	return CleanupOutcome{Result: "deleted"}, nil
}

package safety

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// validateDarwinCanonicalPath checks path shape, absolute requirements,
// and ensures run root paths do not contain seatbelt-breaking characters.
func validateDarwinCanonicalPath(path string) error {
	if err := darwinPathShapeError(path); err != nil {
		return err
	}
	if strings.ContainsAny(path, "()\"\\") {
		return fmt.Errorf("%w: path contains characters invalid for seatbelt", ErrRefused)
	}
	if path == "/tmp" || strings.HasPrefix(path, "/tmp/") {
		return fmt.Errorf("%w: path %q must use canonical /private/tmp prefix on Darwin", ErrRefused, path)
	}
	if path == "/var" || strings.HasPrefix(path, "/var/") {
		return fmt.Errorf("%w: path %q must use canonical /private/var prefix on Darwin", ErrRefused, path)
	}
	return nil
}

// buildDarwinSandboxExecArgv constructs the restricted launcher invocation.
func buildDarwinSandboxExecArgv(sandboxExecPath, profile, selfExe string, isolateArgs []string) ([]string, error) {
	if strings.TrimSpace(sandboxExecPath) == "" {
		return nil, fmt.Errorf("%w: missing sandbox-exec path", ErrRefused)
	}
	if strings.TrimSpace(profile) == "" {
		return nil, fmt.Errorf("%w: missing seatbelt profile", ErrRefused)
	}
	if strings.TrimSpace(selfExe) == "" {
		return nil, fmt.Errorf("%w: missing target executable", ErrRefused)
	}
	argv := []string{sandboxExecPath, "-p", profile, selfExe}
	argv = append(argv, isolateArgs...)
	return argv, nil
}

// darwinProfileIdentity builds the identity record for Darwin profile hashing.
func darwinProfileIdentity(runID, rootPath, sentinelPath string, dev, ino, sdev, sino uint64) profileIdentity {
	return profileIdentity{
		RunID:       runID,
		Root:        rootPath,
		Dev:         dev,
		Ino:         ino,
		Sentinel:    sentinelPath,
		SentinelDev: sdev,
		SentinelIno: sino,
	}
}

// checkDarwinSentinelProbe verifies the probe operations against a synthetic sentinel.
func checkDarwinSentinelProbe(sentinelPath string, writeErr, removeErr, createErr error, before, after []byte) error {
	if !isDarwinDenied(writeErr) {
		return fmt.Errorf("sentinel_write_not_denied: %v", writeErr)
	}
	if !isDarwinDenied(removeErr) {
		return fmt.Errorf("sentinel_remove_not_denied: %v", removeErr)
	}
	if !isDarwinDenied(createErr) {
		return fmt.Errorf("sentinel_create_sibling_not_denied: %v", createErr)
	}
	if string(before) != string(after) {
		return fmt.Errorf("sentinel_changed")
	}
	return nil
}

func isDarwinDenied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || os.IsPermission(err) {
		return true
	}
	return false
}

// parseDarwinConfirmMessage parses the helper confirmation message.
func parseDarwinConfirmMessage(msg string) (bool, string, error) {
	trimmed := strings.TrimSpace(msg)
	if strings.HasPrefix(trimmed, "OK") {
		return true, "OK", nil
	}
	if strings.HasPrefix(trimmed, "FAIL") {
		reason := strings.TrimSpace(strings.TrimPrefix(trimmed, "FAIL"))
		if reason == "" {
			reason = "verify_failed"
		}
		return false, reason, fmt.Errorf("verification failed: %s", reason)
	}
	if trimmed == "" {
		return false, "helper closed confirm pipe before OK", fmt.Errorf("verification failed: helper closed confirm pipe before OK")
	}
	return false, trimmed, fmt.Errorf("verification failed: %s", trimmed)
}

// validateDarwinCleanupMarker verifies the directory marker against recorded ownership.
func validateDarwinCleanupMarker(marker, rec RunRecord, dev, ino uint64) error {
	if marker.RunID != rec.RunID {
		return fmt.Errorf("%w: marker run_id mismatch", ErrCleanupBlocked)
	}
	if marker.Nonce != rec.Nonce {
		return fmt.Errorf("%w: marker nonce mismatch", ErrCleanupBlocked)
	}
	if marker.Root != rec.Root {
		return fmt.Errorf("%w: marker root mismatch", ErrCleanupBlocked)
	}
	if marker.Dev != dev || marker.Ino != ino {
		return fmt.Errorf("%w: marker device/inode mismatch", ErrCleanupBlocked)
	}
	return nil
}

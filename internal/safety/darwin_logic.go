package safety

import (
	"errors"
	"fmt"
	"os"
	"path"
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

// darwinConfirmExecSeam captures the low-level confirmation and execution operations.
type darwinConfirmExecSeam struct {
	Write func(fd int, b []byte) (int, error)
	Close func(fd int) error
	Exec  func(argv0 string, argv []string, env []string) error
}

// darwinConfirmAndExec coordinates confirmation writing, pipe closing, and user exec.
// It fails closed and refuses exec on write error, short write, or close error.
func darwinConfirmAndExec(confirmFD int, user []string, env []string, seam darwinConfirmExecSeam) error {
	if len(user) == 0 || strings.TrimSpace(user[0]) == "" {
		return fmt.Errorf("%w: empty user command", ErrRefused)
	}
	if seam.Write == nil || seam.Close == nil || seam.Exec == nil {
		return fmt.Errorf("%w: incomplete confirm seam callbacks", ErrRefused)
	}

	msg := []byte("OK\n")
	n, err := seam.Write(confirmFD, msg)
	if err != nil {
		return fmt.Errorf("%w: confirmation write failed: %w", ErrRefused, err)
	}
	if n != len(msg) {
		return fmt.Errorf("%w: confirmation short write (wrote %d of %d bytes)", ErrRefused, n, len(msg))
	}
	if err := seam.Close(confirmFD); err != nil {
		return fmt.Errorf("%w: confirmation close failed: %w", ErrRefused, err)
	}
	return seam.Exec(user[0], user, env)
}

// DarwinDirPlan represents a validated directory creation plan.
type DarwinDirPlan struct {
	TargetDir        string
	ExistingAncestor string
	MissingSegments  []string
}

// planDarwinSafeDir validates target against protected homes and allowed boundaries,
// and plans handle-relative creation from the existing ancestor.
func planDarwinSafeDir(target string, homes []string, statDir func(path string) (isDir bool, exists bool, err error)) (DarwinDirPlan, error) {
	if err := darwinPathShapeError(target); err != nil {
		return DarwinDirPlan{}, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	if strings.ContainsAny(target, "()\"\\") {
		return DarwinDirPlan{}, fmt.Errorf("%w: target path contains injection characters", ErrRefused)
	}
	if target == "/tmp" || strings.HasPrefix(target, "/tmp/") {
		return DarwinDirPlan{}, fmt.Errorf("%w: path %q must use canonical /private/tmp prefix on Darwin", ErrRefused, target)
	}
	if target == "/var" || strings.HasPrefix(target, "/var/") {
		return DarwinDirPlan{}, fmt.Errorf("%w: path %q must use canonical /private/var prefix on Darwin", ErrRefused, target)
	}
	cleanTarget := path.Clean(target)
	for _, home := range homes {
		if strings.TrimSpace(home) != "" && withinPath(home, cleanTarget) {
			return DarwinDirPlan{}, fmt.Errorf("%w: target %q is inside protected home %q", ErrRefused, cleanTarget, home)
		}
	}

	curr := cleanTarget
	var missing []string
	for curr != "/" && curr != "." && curr != "" {
		isDir, exists, err := statDir(curr)
		if err != nil {
			return DarwinDirPlan{}, err
		}
		if exists {
			if !isDir {
				return DarwinDirPlan{}, fmt.Errorf("%w: ancestor %q is not a directory", ErrRefused, curr)
			}
			break
		}
		missing = append([]string{path.Base(curr)}, missing...)
		parent := path.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	if curr == "" || curr == "." {
		curr = "/"
	}

	for _, home := range homes {
		if strings.TrimSpace(home) != "" && withinPath(home, curr) {
			return DarwinDirPlan{}, fmt.Errorf("%w: ancestor %q is inside protected home %q", ErrRefused, curr, home)
		}
	}

	walk := curr
	for _, seg := range missing {
		walk = path.Join(walk, seg)
		for _, home := range homes {
			if strings.TrimSpace(home) != "" && withinPath(home, walk) {
				return DarwinDirPlan{}, fmt.Errorf("%w: prospective path %q is inside protected home %q", ErrRefused, walk, home)
			}
		}
	}

	return DarwinDirPlan{
		TargetDir:        cleanTarget,
		ExistingAncestor: curr,
		MissingSegments:  missing,
	}, nil
}

// executeDarwinSafeDirPlan executes the planned creation steps through the mutator.
func executeDarwinSafeDirPlan(plan DarwinDirPlan, mkdirSeg func(parent string, seg string) error) error {
	parent := plan.ExistingAncestor
	for _, seg := range plan.MissingSegments {
		if err := mkdirSeg(parent, seg); err != nil {
			return err
		}
		parent = path.Join(parent, seg)
	}
	return nil
}

// DarwinTargetIdentity records target identity attributes.
type DarwinTargetIdentity struct {
	Dev uint64
	Ino uint64
}

// DarwinFixtureLinkPlan holds paths for the synthetic directory link escape fixture.
type DarwinFixtureLinkPlan struct {
	RunRoot           string
	ExternalTargetDir string
	ExternalPayload   string
	LinkInWork        string
	EscapeThroughLink string
}

// planDarwinFixtureLink constructs and validates the synthetic link test layout.
func planDarwinFixtureLink(base string, runRoot string) (DarwinFixtureLinkPlan, error) {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(runRoot) == "" {
		return DarwinFixtureLinkPlan{}, fmt.Errorf("empty base or runRoot")
	}
	cleanBase := path.Clean(base)
	cleanRoot := path.Clean(runRoot)
	extTarget := path.Join(cleanBase, "synthetic-external-target")
	if withinPath(cleanRoot, extTarget) || extTarget == cleanRoot {
		return DarwinFixtureLinkPlan{}, fmt.Errorf("external target must be strictly outside run root")
	}
	link := path.Join(cleanRoot, "work", "dir-link-escape")
	if !withinPath(cleanRoot, link) {
		return DarwinFixtureLinkPlan{}, fmt.Errorf("symlink must be inside run root")
	}
	return DarwinFixtureLinkPlan{
		RunRoot:           cleanRoot,
		ExternalTargetDir: extTarget,
		ExternalPayload:   path.Join(extTarget, "target.txt"),
		LinkInWork:        link,
		EscapeThroughLink: path.Join(link, "escape-attempt.txt"),
	}, nil
}

// verifyDarwinFixtureLinkEscape evaluates the escape output and cleanup outcomes.
func verifyDarwinFixtureLinkEscape(escapeOutput string, plan DarwinFixtureLinkPlan, beforeIdent, afterIdent DarwinTargetIdentity, targetContent []byte, rootExists, linkExists, targetExists bool) error {
	if !strings.Contains(escapeOutput, "path="+plan.EscapeThroughLink) || !strings.Contains(escapeOutput, "write=eperm") {
		return fmt.Errorf("escape through directory link was not denied with eperm")
	}
	if !targetExists {
		return fmt.Errorf("outside target was destroyed")
	}
	if string(targetContent) != "outside-target-v1\n" {
		return fmt.Errorf("outside target content was altered")
	}
	if beforeIdent.Dev != 0 && (afterIdent.Dev != beforeIdent.Dev || afterIdent.Ino != beforeIdent.Ino) {
		return fmt.Errorf("outside target identity changed (dev/ino mismatch)")
	}
	if rootExists {
		return fmt.Errorf("run root was not reclaimed")
	}
	if linkExists {
		return fmt.Errorf("symlink was not reclaimed")
	}
	return nil
}

// handleDarwinRegistryRemoval handles registry deletion after root unlink.
func handleDarwinRegistryRemoval(registryPath string, rootPath string, removeFn func(string) error) (CleanupOutcome, error) {
	out := CleanupOutcome{
		Targets: []string{rootPath},
	}
	if registryPath == "" {
		out.Result = "deleted"
		return out, nil
	}
	if removeFn == nil {
		removeFn = os.Remove
	}
	if err := removeFn(registryPath); err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			out.Result = "deleted"
			return out, nil
		}
		out.Result = "partial"
		out.Reason = fmt.Sprintf("root removed, registry retained: %v", err)
		return out, fmt.Errorf("%w: %s: %w", ErrCleanupBlocked, out.Reason, err)
	}
	out.Result = "deleted"
	return out, nil
}

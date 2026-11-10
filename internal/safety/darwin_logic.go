package safety

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
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
	RunRoot             string
	ExternalTargetDir   string
	ExternalPayload     string
	LinkInWork          string
	EscapeThroughLink   string
	ExistingThroughLink string
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
		RunRoot:             cleanRoot,
		ExternalTargetDir:   extTarget,
		ExternalPayload:     path.Join(extTarget, "target.txt"),
		LinkInWork:          link,
		EscapeThroughLink:   path.Join(link, "escape-attempt.txt"),
		ExistingThroughLink: path.Join(link, "target.txt"),
	}, nil
}

// DarwinEscapeRecord holds the outcomes of a worker probe for a specific path.
type DarwinEscapeRecord struct {
	Path   string
	Write  string
	Remove string
	Read   string
}

// DarwinSyntheticProbeProof proves that a file was synthetically created during this run.
type DarwinSyntheticProbeProof struct {
	CanonicalPath string
	Dev           uint64
	Ino           uint64
	RunID         string
	Root          string
	Nonce         string
	Minted        bool
}

type darwinPendingSyntheticProbe struct {
	CanonicalPath string
	Dev           uint64
	Ino           uint64
}

// DarwinImageProof proves that an executable was validated for this run.
// Nonce is required and must be the exact non-empty run nonce. An empty
// nonce is not a proof, even when run, root, and hash are present.
type DarwinImageProof struct {
	CanonicalPath string
	Dev           uint64
	Ino           uint64
	RunID         string
	Root          string
	Nonce         string
	ExeSHA256     string
	Minted        bool
}

var (
	darwinProbeMu         sync.Mutex
	darwinSyntheticProofs = make(map[string]DarwinSyntheticProbeProof)
	darwinPendingProbes   = make(map[string]darwinPendingSyntheticProbe)
	darwinImageProofs     = make(map[string]DarwinImageProof)
)

func clearDarwinRegistries() {
	darwinProbeMu.Lock()
	defer darwinProbeMu.Unlock()
	darwinSyntheticProofs = make(map[string]DarwinSyntheticProbeProof)
	darwinPendingProbes = make(map[string]darwinPendingSyntheticProbe)
	darwinImageProofs = make(map[string]DarwinImageProof)
}

// registerPendingDarwinSyntheticProbe records the fresh exclusive synthetic creation of a sentinel
// before the run ID is generated. It validates canonical path, protected boundaries, and nonzero identity.
func registerPendingDarwinSyntheticProbe(probePath string, dev, ino uint64) error {
	if strings.TrimSpace(probePath) == "" {
		return fmt.Errorf("%w: empty synthetic probe path", ErrRefused)
	}
	if err := validateDarwinCanonicalPath(probePath); err != nil {
		return fmt.Errorf("%w: synthetic probe path not canonical: %v", ErrRefused, err)
	}
	clean := path.Clean(probePath)
	if clean != probePath || !path.IsAbs(probePath) {
		return fmt.Errorf("%w: synthetic probe path not canonical: %s", ErrRefused, probePath)
	}
	if strings.HasPrefix(clean, "/Volumes/") || strings.HasPrefix(clean, "/Users/") || strings.HasPrefix(clean, "/System/Volumes/Data/Users/") {
		return fmt.Errorf("%w: synthetic probe cannot be in home/volume: %s", ErrRefused, clean)
	}
	for _, sens := range []string{".ssh", "Keychains", "login.keychain", "credentials", ".config", ".aws", ".gnupg"} {
		for _, part := range strings.Split(clean, "/") {
			if part == sens {
				return fmt.Errorf("%w: synthetic probe contains sensitive segment %s", ErrRefused, sens)
			}
		}
	}
	if dev == 0 || ino == 0 {
		return fmt.Errorf("%w: synthetic probe has zero device or inode", ErrSentinelUnusable)
	}
	darwinProbeMu.Lock()
	defer darwinProbeMu.Unlock()
	darwinPendingProbes[clean] = darwinPendingSyntheticProbe{
		CanonicalPath: clean,
		Dev:           dev,
		Ino:           ino,
	}
	return nil
}

// bindDarwinSyntheticProbe transitions an exclusive pending synthetic sentinel into an authoritative
// one-run bound proof. If sentPath was not pending, it is a no-op (caller-provided sentinel
// is not blessed). If pending identity mismatches or run ID/root/nonce are empty, it fails closed.
func bindDarwinSyntheticProbe(runID, rootPath, nonce, sentPath string, sdev, sino uint64) error {
	clean := path.Clean(sentPath)
	darwinProbeMu.Lock()
	defer darwinProbeMu.Unlock()
	pending, ok := darwinPendingProbes[clean]
	if !ok {
		// Not a pending synthetic probe created by this process, or already consumed.
		return nil
	}
	// Atomic consume: exclusive one-run transition. Immediately delete pending probe
	// so that no subsequent or racing consumer can ever bind it.
	delete(darwinPendingProbes, clean)

	if pending.Dev != sdev || pending.Ino != sino || pending.CanonicalPath != clean {
		return fmt.Errorf("%w: pending probe identity mismatch (dev/ino)", ErrSentinelUnusable)
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(rootPath) == "" || strings.TrimSpace(nonce) == "" || sdev == 0 || sino == 0 {
		return fmt.Errorf("%w: incomplete run identity for probe binding", ErrSentinelUnusable)
	}
	if err := validateDarwinCanonicalPath(rootPath); err != nil {
		return fmt.Errorf("%w: invalid root path for probe binding: %v", ErrSentinelUnusable, err)
	}
	proof := DarwinSyntheticProbeProof{
		CanonicalPath: clean,
		Dev:           sdev,
		Ino:           sino,
		RunID:         runID,
		Root:          path.Clean(rootPath),
		Nonce:         nonce,
		Minted:        true,
	}
	darwinSyntheticProofs[clean] = proof
	return nil
}

func isVerifiedDarwinSyntheticProbe(p string, root string) bool {
	clean := path.Clean(p)
	cleanRoot := path.Clean(root)
	if cleanRoot == "" || cleanRoot == "." || cleanRoot == "/" {
		return false
	}
	darwinProbeMu.Lock()
	defer darwinProbeMu.Unlock()
	proof, ok := darwinSyntheticProofs[clean]
	if !ok || !proof.Minted || proof.CanonicalPath != clean ||
		proof.RunID == "" || proof.Nonce == "" || proof.Dev == 0 || proof.Ino == 0 ||
		proof.Root == "" || proof.Root != cleanRoot {
		return false
	}
	return true
}

func isVerifiedDarwinImage(p string, root string) bool {
	clean := path.Clean(p)
	cleanRoot := path.Clean(root)
	if cleanRoot == "" || cleanRoot == "." || cleanRoot == "/" {
		return false
	}
	darwinProbeMu.Lock()
	defer darwinProbeMu.Unlock()
	proof, ok := darwinImageProofs[clean]
	if !ok || !proof.Minted || proof.CanonicalPath != clean ||
		strings.TrimSpace(proof.RunID) == "" || strings.TrimSpace(proof.Nonce) == "" ||
		strings.TrimSpace(proof.ExeSHA256) == "" || proof.Dev == 0 || proof.Ino == 0 ||
		proof.Root == "" || proof.Root != cleanRoot {
		return false
	}
	return true
}

// darwinImageInspectorSeam abstracts low-level OS operations for inspecting an executable image.
type darwinImageInspectorSeam func(exePath string) (canonicalPath string, dev, ino uint64, isReg bool, sha256Hex string, err error)

var (
	darwinImageInspectorMu sync.Mutex
	darwinImageInspector   darwinImageInspectorSeam = defaultDarwinImageInspector
)

func setDarwinImageInspector(fn darwinImageInspectorSeam) func() {
	darwinImageInspectorMu.Lock()
	defer darwinImageInspectorMu.Unlock()
	orig := darwinImageInspector
	darwinImageInspector = fn
	return func() {
		darwinImageInspectorMu.Lock()
		defer darwinImageInspectorMu.Unlock()
		darwinImageInspector = orig
	}
}

func getDarwinImageInspector() darwinImageInspectorSeam {
	darwinImageInspectorMu.Lock()
	defer darwinImageInspectorMu.Unlock()
	return darwinImageInspector
}

// resolveDarwinAllowedReads strictly resolves read permissions for confined Darwin launch.
// Caller sentinels, unverified caller paths, custom HOME, and paths with sensitive segments
// are rejected. Only valid target executable and verified this-run synthetic probes are allowed.
func resolveDarwinAllowedReads(rec RunRecord, selfExe string, homes []string) ([]string, error) {
	if strings.TrimSpace(selfExe) == "" {
		return nil, fmt.Errorf("%w: missing self executable", ErrRefused)
	}
	if err := validateDarwinCanonicalPath(selfExe); err != nil {
		return nil, fmt.Errorf("%w: target executable is not canonical: %v", ErrRefused, err)
	}
	cleanExe := path.Clean(selfExe)
	if cleanExe != selfExe || !path.IsAbs(selfExe) {
		return nil, fmt.Errorf("%w: target executable is not canonical: %q", ErrRefused, selfExe)
	}
	if strings.TrimSpace(rec.RunID) == "" || strings.TrimSpace(rec.Root) == "" || strings.TrimSpace(rec.Nonce) == "" || strings.TrimSpace(rec.ExeSHA256) == "" {
		return nil, fmt.Errorf("%w: incomplete run record for image verification", ErrRefused)
	}
	if err := validateDarwinCanonicalPath(rec.Root); err != nil {
		return nil, fmt.Errorf("%w: run root is not canonical: %v", ErrRefused, err)
	}

	inspector := getDarwinImageInspector()
	if inspector == nil {
		return nil, fmt.Errorf("%w: image inspector unavailable", ErrRefused)
	}
	canonExe, dev, ino, isReg, actualSHA, err := inspector(cleanExe)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect target executable: %v", ErrRefused, err)
	}
	if !isReg {
		return nil, fmt.Errorf("%w: target executable is not a regular file", ErrRefused)
	}
	if dev == 0 || ino == 0 {
		return nil, fmt.Errorf("%w: target executable has zero device or inode", ErrRefused)
	}
	if strings.TrimSpace(canonExe) == "" {
		return nil, fmt.Errorf("%w: target executable has empty canonical path", ErrRefused)
	}
	if err := validateDarwinCanonicalPath(canonExe); err != nil {
		return nil, fmt.Errorf("%w: canonical target executable path invalid: %v", ErrRefused, err)
	}
	cleanCanon := path.Clean(canonExe)
	if cleanCanon != canonExe || !path.IsAbs(canonExe) {
		return nil, fmt.Errorf("%w: canonical target executable path not absolute: %s", ErrRefused, canonExe)
	}

	for _, home := range homes {
		if strings.TrimSpace(home) != "" && (withinPath(home, cleanCanon) || cleanCanon == home) {
			return nil, fmt.Errorf("%w: target executable cannot be inside protected home %q", ErrRefused, home)
		}
	}
	for _, forbidden := range []string{
		"/Users",
		"/System/Volumes/Data/Users",
		"/Library/Keychains",
		"/private/var/root",
		"/private/var/db/dslocal",
	} {
		if withinPath(forbidden, cleanCanon) || cleanCanon == forbidden {
			return nil, fmt.Errorf("%w: target executable is inside protected path %q", ErrRefused, forbidden)
		}
	}
	if strings.HasPrefix(cleanCanon, "/Volumes/") {
		return nil, fmt.Errorf("%w: target executable is in external volume: %s", ErrRefused, cleanCanon)
	}
	for _, sens := range []string{".ssh", "Keychains", "login.keychain", "credentials", ".config", ".aws", ".gnupg"} {
		for _, part := range strings.Split(cleanCanon, "/") {
			if part == sens {
				return nil, fmt.Errorf("%w: target executable contains sensitive segment %s", ErrRefused, sens)
			}
		}
	}

	if actualSHA != rec.ExeSHA256 {
		return nil, fmt.Errorf("%w: target executable hash mismatch: got %s, want %s", ErrRefused, actualSHA, rec.ExeSHA256)
	}

	// Record verified image proof
	imageProof := DarwinImageProof{
		CanonicalPath: cleanCanon,
		Dev:           dev,
		Ino:           ino,
		RunID:         rec.RunID,
		Root:          path.Clean(rec.Root),
		Nonce:         rec.Nonce,
		ExeSHA256:     actualSHA,
		Minted:        true,
	}
	darwinProbeMu.Lock()
	darwinImageProofs[cleanCanon] = imageProof
	darwinProbeMu.Unlock()

	allowed := []string{cleanCanon}

	// Only an owned, verified this-run synthetic probe can be granted read permission.
	// Exact nonempty run/root/nonce and matching nonzero sentinel identity required; no empty wildcard.
	if rec.Sentinel != "" && strings.TrimSpace(rec.RunID) != "" && strings.TrimSpace(rec.Nonce) != "" &&
		strings.TrimSpace(rec.Root) != "" && rec.SentinelDev != 0 && rec.SentinelIno != 0 {
		cleanSent := path.Clean(rec.Sentinel)
		if cleanSent == rec.Sentinel && path.IsAbs(rec.Sentinel) {
			darwinProbeMu.Lock()
			proof, ok := darwinSyntheticProofs[cleanSent]
			darwinProbeMu.Unlock()
			if ok && proof.Minted && proof.CanonicalPath == cleanSent &&
				proof.RunID == rec.RunID && proof.Nonce == rec.Nonce &&
				proof.Dev == rec.SentinelDev && proof.Ino == rec.SentinelIno &&
				proof.Root == path.Clean(rec.Root) {
				inHome := false
				for _, home := range homes {
					if strings.TrimSpace(home) != "" && (withinPath(home, cleanSent) || cleanSent == home) {
						inHome = true
						break
					}
				}
				if !inHome && !withinPath(rec.Root, cleanSent) && cleanSent != rec.Root {
					allowed = append(allowed, cleanSent)
				}
			}
		}
	}
	return allowed, nil
}

// darwinPathAncestors computes the literal directory ancestors of a path up to root "/".
func darwinPathAncestors(p string) []string {
	clean := path.Clean(p)
	if clean == "" || clean == "." || clean == "/" {
		return []string{"/"}
	}
	var ancestors []string
	curr := clean
	for curr != "/" && curr != "." && curr != "" {
		ancestors = append([]string{curr}, ancestors...)
		parent := path.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	ancestors = append([]string{"/"}, ancestors...)
	return ancestors
}

// parseDarwinEscapeRecords parses escape.txt into per-path records.
// It strictly binds every path, action, and outcome:
// - required fields (path, write, remove, read) must appear exactly once per record
// - duplicate, missing, unknown, or contradictory records are rejected
func parseDarwinEscapeRecords(text string) (map[string]DarwinEscapeRecord, error) {
	lines := strings.Split(text, "\n")
	records := make(map[string]DarwinEscapeRecord)

	type fieldPresence struct {
		hasWrite  bool
		hasRemove bool
		hasRead   bool
	}

	var curr DarwinEscapeRecord
	var seen fieldPresence
	inRecord := false

	flushRecord := func() error {
		if !inRecord {
			return nil
		}
		if curr.Path == "" {
			return fmt.Errorf("record missing path")
		}
		if !seen.hasWrite {
			return fmt.Errorf("record for %q missing write field", curr.Path)
		}
		if !seen.hasRemove {
			return fmt.Errorf("record for %q missing remove field", curr.Path)
		}
		if !seen.hasRead {
			return fmt.Errorf("record for %q missing read field", curr.Path)
		}
		if err := validateDarwinEscapeRecord(curr); err != nil {
			return err
		}
		if _, exists := records[curr.Path]; exists {
			return fmt.Errorf("duplicate record for %q", curr.Path)
		}
		records[curr.Path] = curr
		curr = DarwinEscapeRecord{}
		seen = fieldPresence{}
		inRecord = false
		return nil
	}

	for lineNum, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("line %d: malformed record line %q", lineNum+1, line)
		}
		key := line[:idx]
		val := line[idx+1:]

		switch key {
		case "path":
			if inRecord {
				if err := flushRecord(); err != nil {
					return nil, err
				}
			}
			cleanPath := path.Clean(val)
			if cleanPath == "" || cleanPath == "." {
				return nil, fmt.Errorf("line %d: empty or invalid path %q", lineNum+1, val)
			}
			curr.Path = val
			inRecord = true

		case "write":
			if !inRecord {
				return nil, fmt.Errorf("line %d: write field before path", lineNum+1)
			}
			if seen.hasWrite {
				return nil, fmt.Errorf("line %d: duplicate write field for %q", lineNum+1, curr.Path)
			}
			curr.Write = val
			seen.hasWrite = true

		case "remove":
			if !inRecord {
				return nil, fmt.Errorf("line %d: remove field before path", lineNum+1)
			}
			if seen.hasRemove {
				return nil, fmt.Errorf("line %d: duplicate remove field for %q", lineNum+1, curr.Path)
			}
			curr.Remove = val
			seen.hasRemove = true

		case "read":
			if !inRecord {
				return nil, fmt.Errorf("line %d: read field before path", lineNum+1)
			}
			if seen.hasRead {
				return nil, fmt.Errorf("line %d: duplicate read field for %q", lineNum+1, curr.Path)
			}
			curr.Read = val
			seen.hasRead = true

		default:
			return nil, fmt.Errorf("line %d: unknown field %q for record %q", lineNum+1, key, curr.Path)
		}
	}

	if inRecord {
		if err := flushRecord(); err != nil {
			return nil, err
		}
	}

	return records, nil
}

func validateDarwinEscapeRecord(r DarwinEscapeRecord) error {
	if strings.TrimSpace(r.Path) == "" {
		return fmt.Errorf("record missing path")
	}
	isValidErrno := func(v string) bool {
		return v == "eperm" || v == "enoent" || v == "nil" || v == "other"
	}
	if !isValidErrno(r.Write) {
		return fmt.Errorf("record for %q has invalid write outcome %q", r.Path, r.Write)
	}
	if !isValidErrno(r.Remove) {
		return fmt.Errorf("record for %q has invalid remove outcome %q", r.Path, r.Remove)
	}
	if !isValidErrno(r.Read) {
		return fmt.Errorf("record for %q has invalid read outcome %q", r.Path, r.Read)
	}
	return nil
}

// verifyDarwinFixtureLinkEscape evaluates the escape output and cleanup outcomes.
func verifyDarwinFixtureLinkEscape(escapeOutput string, plan DarwinFixtureLinkPlan, beforeIdent, afterIdent DarwinTargetIdentity, targetContent []byte, rootExists, linkExists, targetExists bool) error {
	records, err := parseDarwinEscapeRecords(escapeOutput)
	if err != nil {
		return fmt.Errorf("failed to parse escape output: %w", err)
	}
	linkRec, ok := records[plan.EscapeThroughLink]
	if !ok {
		return fmt.Errorf("link escape path %q missing from escape records", plan.EscapeThroughLink)
	}
	if linkRec.Write != "eperm" {
		return fmt.Errorf("escape through directory link was not denied with eperm (write=%s)", linkRec.Write)
	}
	// escape-attempt.txt is not created. After a refused create, read and remove
	// of that name are absence (enoent) or a denied lookup (eperm). A nil read
	// would mean bytes came back. That is not the same check as the existing file.
	if linkRec.Read != "enoent" && linkRec.Read != "eperm" {
		return fmt.Errorf("never-created name through directory link was readable or unclassified (read=%s)", linkRec.Read)
	}
	if linkRec.Remove != "enoent" && linkRec.Remove != "eperm" {
		return fmt.Errorf("never-created name through directory link remove was not absence or denial (remove=%s)", linkRec.Remove)
	}
	if plan.ExistingThroughLink != "" {
		existRec, ok := records[plan.ExistingThroughLink]
		if !ok {
			return fmt.Errorf("existing outside target through directory link %q missing from escape records", plan.ExistingThroughLink)
		}
		if existRec.Read != "eperm" {
			return fmt.Errorf("existing outside target read through directory link was not denied with eperm (read=%s)", existRec.Read)
		}
		if existRec.Write != "eperm" {
			return fmt.Errorf("existing outside target write through directory link was not denied with eperm (write=%s)", existRec.Write)
		}
		if existRec.Remove != "eperm" {
			return fmt.Errorf("existing outside target remove through directory link was not denied with eperm (remove=%s)", existRec.Remove)
		}
	}
	if !targetExists {
		return fmt.Errorf("outside target was destroyed")
	}
	if string(targetContent) != "outside-target-v1\n" {
		return fmt.Errorf("outside target content was altered")
	}
	if beforeIdent.Dev == 0 || beforeIdent.Ino == 0 {
		return fmt.Errorf("outside target baseline identity was zero or unrecorded")
	}
	if afterIdent.Dev == 0 || afterIdent.Ino == 0 {
		return fmt.Errorf("outside target after-run identity was zero or unrecorded")
	}
	if afterIdent.Dev != beforeIdent.Dev || afterIdent.Ino != beforeIdent.Ino {
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

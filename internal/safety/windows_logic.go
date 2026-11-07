package safety

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
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

// CreationRecord is the proof that this process just created a fixture directory.
// Created and the unexported proof are set only by mintCreation. A directory
// name, a Temp prefix, or a caller-filled struct is not a record.
type CreationRecord struct {
	Path          string
	CanonicalRoot string
	Volume        uint64
	FileIndex     uint64
	Nonce         string
	Created       bool
	proof         string
}

var creationProofs sync.Map

// mintCreation registers a record created by the function that just made the
// directory. Other packages cannot set the unexported proof.
func mintCreation(rec CreationRecord) (CreationRecord, error) {
	if rec.CanonicalRoot == "" || rec.Nonce == "" || (rec.Volume == 0 && rec.FileIndex == 0) {
		return CreationRecord{}, fmt.Errorf("%w: incomplete creation record", ErrSentinelUnusable)
	}
	proof, err := randomHex(16)
	if err != nil {
		return CreationRecord{}, err
	}
	rec.Created = true
	rec.proof = proof
	creationProofs.Store(proof, rec)
	return rec, nil
}

// canonicalWinPath puts DOS and \\?\ forms in one namespace.
// filepath.Rel does not: \\?\C:\... and C:\... have different volume names.
func canonicalWinPath(p string) string {
	p = strings.ReplaceAll(p, `/`, `\`)
	upper := strings.ToUpper(p)
	switch {
	case strings.HasPrefix(upper, `\\?\UNC\`):
		p = `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`):
		p = p[len(`\\?\`):]
	}
	parts := splitPath(p)
	vol := ""
	if len(parts) > 0 && strings.HasSuffix(parts[0], ":") {
		vol = parts[0]
		parts = parts[1:]
	}
	var stack []string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		stack = append(stack, part)
	}
	out := strings.ToLower(vol)
	for _, part := range stack {
		out += `\` + strings.ToLower(part)
	}
	if out == "" {
		return `\`
	}
	return out
}

func withinCanonical(root, child string) bool {
	r := canonicalWinPath(root)
	c := canonicalWinPath(child)
	if r == "" || c == "" || r == `\` || c == r {
		return false
	}
	return strings.HasPrefix(c, r+`\`)
}

// bindSentinel accepts a protected sentinel only when a creation record from
// this run says so, and the canonical child path is inside that root.
func bindSentinel(rec CreationRecord, sentinel string, volume, index uint64) error {
	if !protectedPathComponent(sentinel) {
		return nil
	}
	loaded, ok := creationProofs.Load(rec.proof)
	if !ok || rec.proof == "" {
		return fmt.Errorf("%w: protected sentinel has no this-run creation record", ErrSentinelUnusable)
	}
	stored := loaded.(CreationRecord)
	if !stored.Created || stored.Nonce != rec.Nonce || stored.Volume != rec.Volume || stored.FileIndex != rec.FileIndex || stored.CanonicalRoot != rec.CanonicalRoot {
		return fmt.Errorf("%w: protected sentinel creation record was not minted for this directory", ErrSentinelUnusable)
	}
	if protectedPathComponent(rec.CanonicalRoot) {
		return fmt.Errorf("%w: creation record root is itself a protected path", ErrSentinelUnusable)
	}
	if !withinCanonical(rec.CanonicalRoot, sentinel) {
		return fmt.Errorf("%w: protected sentinel is outside the creation record", ErrSentinelUnusable)
	}
	if volume == rec.Volume && index == rec.FileIndex {
		return fmt.Errorf("%w: sentinel file identity is the fixture root", ErrSentinelUnusable)
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

// FileBinding is the parent or child view of one sentinel.
type FileBinding struct {
	Path   string
	Volume uint64
	Index  uint64
}

// hashSentinelBinding keeps the parent's path and file identity when the
// child cannot read sentinel metadata. It does not substitute zeros.
func hashSentinelBinding(observedOK bool, observed, parent FileBinding) (FileBinding, error) {
	if !observedOK {
		if parent.Path == "" || (parent.Volume == 0 && parent.Index == 0) {
			return FileBinding{}, fmt.Errorf("sentinel_identity")
		}
		return parent, nil
	}
	if canonicalWinPath(parent.Path) != canonicalWinPath(observed.Path) || observed.Volume != parent.Volume || observed.Index != parent.Index {
		return FileBinding{}, fmt.Errorf("sentinel_identity")
	}
	// Hash the parent spelling. A \\?\ versus DOS spelling of the same file
	// must not change the profile hash.
	return parent, nil
}

const (
	statusPending        = 0x00000103
	statusNoMoreFilesNT  = 0x80000006
	statusBufferOverflow = 0x80000005
)

// ntQueryResult rejects pending I/O and information lengths outside the buffer.
// STATUS_PENDING is non-negative, so a sign check alone treats it as success.
func ntQueryResult(status uint32, information uintptr, bufLen int) (int, error) {
	if status == statusPending {
		return 0, fmt.Errorf("BLOCKED: NT I/O still pending")
	}
	if status == statusNoMoreFilesNT {
		return 0, errNTNoMore
	}
	if status != 0 && status != statusBufferOverflow {
		return 0, fmt.Errorf("BLOCKED: NT status 0x%08X", status)
	}
	n := int(information)
	if n < 0 || n > bufLen {
		return 0, fmt.Errorf("BLOCKED: NT information length %d outside buffer %d", n, bufLen)
	}
	return n, nil
}

// ntCreateStatus accepts only a completed success. STATUS_PENDING is
// non-negative, and a directory status such as STATUS_NO_MORE_FILES is not
// a created file.
func ntCreateStatus(status uint32) error {
	if status == statusPending {
		return fmt.Errorf("BLOCKED: NT I/O still pending")
	}
	if status != 0 {
		return fmt.Errorf("BLOCKED: NT status 0x%08X", status)
	}
	return nil
}

// errNTNoMore ends a directory listing. It is not a cleanup failure.
var errNTNoMore = fmt.Errorf("no more directory entries")

// mountPointReparse builds a mount-point reparse buffer whose print-name
// offset and length stay inside the buffer. Substitute and print names are
// stored with a UTF-16 NUL between them; lengths exclude that NUL.
func mountPointReparse(subst, printName []uint16) ([]byte, error) {
	subst = trimNUL(subst)
	printName = trimNUL(printName)
	if len(subst) == 0 || len(printName) == 0 {
		return nil, fmt.Errorf("empty reparse name")
	}
	printOff := (len(subst) + 1) * 2
	dataLen := 8 + printOff + len(printName)*2 + 2
	buf := make([]byte, 8+dataLen)
	put32(buf[0:], 0xA0000003)
	put16(buf[4:], uint16(dataLen))
	put16(buf[8:], 0)
	put16(buf[10:], uint16(len(subst)*2))
	put16(buf[12:], uint16(printOff))
	put16(buf[14:], uint16(len(printName)*2))
	for i, c := range subst {
		put16(buf[16+i*2:], c)
	}
	base := 16 + printOff
	for i, c := range printName {
		put16(buf[base+i*2:], c)
	}
	end := base + len(printName)*2
	if end > len(buf) || 16+printOff != base {
		return nil, fmt.Errorf("reparse print name does not fit")
	}
	return buf, nil
}

func trimNUL(u []uint16) []uint16 {
	if len(u) > 0 && u[len(u)-1] == 0 {
		return u[:len(u)-1]
	}
	return u
}

func put16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func put32(b []byte, v uint32) { put16(b, uint16(v)); put16(b[2:], uint16(v>>16)) }

// Lifecycle is the evidence required before cleanup.
type Lifecycle struct {
	HelperExited    bool
	ActiveProcesses int
	CancelJoined    bool
	HandlesReleased bool
}

// ErrDescendantsActive means cleanup must not start.
var ErrDescendantsActive = fmt.Errorf("descendants still active")

func cleanupAllowed(s Lifecycle) error {
	if !s.HelperExited || !s.CancelJoined || !s.HandlesReleased {
		return fmt.Errorf("%w: launch is not quiescent", ErrDescendantsActive)
	}
	if s.ActiveProcesses != 0 {
		return fmt.Errorf("%w: %d", ErrDescendantsActive, s.ActiveProcesses)
	}
	return nil
}

// RunManifest is written before the package folder is populated so a failed
// create or a later retry can see the profile this run actually created.
type RunManifest struct {
	RunID          string `json:"run_id"`
	Nonce          string `json:"nonce"`
	ProfileName    string `json:"profile_name"`
	PackageSID     string `json:"package_sid"`
	Root           string `json:"root"`
	RootDev        uint64 `json:"root_dev"`
	RootIno        uint64 `json:"root_ino"`
	SourceSHA256   string `json:"source_sha256"`
	CopySHA256     string `json:"copy_sha256"`
	Phase          string `json:"phase"`
	ProfileCreated bool   `json:"profile_created"`
}

// cleanupPlan chooses profile-only recovery when the owned root is already
// gone. It never selects a profile that this manifest did not create.
func cleanupPlan(rootMissing bool, idMatches bool, m RunManifest) (string, error) {
	if rootMissing {
		if m.ProfileCreated && m.ProfileName != "" && m.Nonce != "" && m.RunID != "" {
			return "profile_only", nil
		}
		return "", fmt.Errorf("%w: root missing and no owned profile manifest", ErrCleanupBlocked)
	}
	if idMatches && (!m.ProfileCreated || (m.ProfileName != "" && m.Nonce != "")) {
		return "root_then_profile", nil
	}
	return "", fmt.Errorf("%w: root identity does not match the creation record", ErrCleanupBlocked)
}

func agreeHashes(source, copy string) error {
	if len(source) != 64 || source != copy || !hex64(source) {
		return fmt.Errorf("BLOCKED: executable copy hash does not match the source hash")
	}
	return nil
}

func hex64(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// pathsConflate reports that child is the parent or inside it. On Windows it
// also treats \\?\ and DOS spellings of one path as the same path. Linux
// keeps byte-for-byte path rules.
func pathsConflate(parent, child string) bool {
	if withinPath(parent, child) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	return withinCanonical(parent, child) || canonicalWinPath(parent) == canonicalWinPath(child)
}

// escapeDenials requires every listed path to record its own write and remove denial.
func escapeDenials(text string, paths []string) error {
	lines := strings.Split(text, "\n")
	for _, p := range paths {
		found := false
		for i := 0; i+2 < len(lines); i++ {
			if lines[i] == "path="+p && lines[i+1] == "write=eperm" && lines[i+2] == "remove=eperm" {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("missing per-path denial for %s", p)
		}
	}
	return nil
}

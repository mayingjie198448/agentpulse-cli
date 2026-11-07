//go:build windows

package safety

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func createSyntheticSentinel(base string) (string, error) {
	dir := filepath.Join(os.TempDir(), "agentpulse-sentinels")
	if stringsTrim(base) != "" {
		parent := filepath.Dir(filepath.Clean(base))
		if parent != "" && parent != "." {
			dir = filepath.Join(parent, "agentpulse-sentinels")
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	name, err := randomHex(8)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "sentinel-"+name)
	if err := os.WriteFile(path, []byte("sentinel-v1\n"), 0600); err != nil {
		return "", err
	}
	kpath, _, err := statNoFollow(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSentinelUnusable, err)
	}
	return kpath, nil
}

func createRunRoot(opts createOpts) (rec RunRecord, err error) {
	kSent, sentID, err := statNoFollow(opts.Sentinel)
	if err != nil {
		if os.IsNotExist(err) {
			return RunRecord{}, fmt.Errorf("%w: sentinel missing (ENOENT); not proof of denied access", ErrSentinelUnusable)
		}
		return RunRecord{}, fmt.Errorf("%w: %v", ErrSentinelUnusable, err)
	}
	if sentID.dir {
		return RunRecord{}, fmt.Errorf("%w: sentinel is not an existing regular file", ErrSentinelUnusable)
	}
	if err := sentinelAllowed(kSent, opts.OwnedRoot); err != nil {
		return RunRecord{}, err
	}
	if opts.OwnedRoot != "" {
		if _, id, err := statNoFollow(opts.OwnedRoot); err != nil || id.reparse || !id.dir {
			return RunRecord{}, fmt.Errorf("%w: owned fixture root is not an ordinary directory", ErrSentinelUnusable)
		}
		if protectedPathComponent(opts.OwnedRoot) || !withinPath(os.TempDir(), opts.OwnedRoot) {
			return RunRecord{}, fmt.Errorf("%w: owned fixture root is not under this run's temp directory", ErrSentinelUnusable)
		}
	}

	runID, err := randomHex(16)
	if err != nil {
		return RunRecord{}, err
	}
	nonce, err := randomHex(16)
	if err != nil {
		return RunRecord{}, err
	}
	prof, err := createAppProfile(runID)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	failed := true
	defer func() {
		freeSID(prof.sid)
		if failed {
			if derr := deleteAppProfile(prof.name); derr != nil {
				err = fmt.Errorf("%v; profile delete failed: %w", err, derr)
			}
		}
	}()

	rootPath, rootID, err := statNoFollow(prof.folder)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: package folder: %v", ErrRefused, err)
	}
	if !rootID.dir {
		return RunRecord{}, fmt.Errorf("%w: package folder is not a directory", ErrRefused)
	}
	if withinPath(rootPath, kSent) || equalFoldPath(rootPath, kSent) {
		return RunRecord{}, fmt.Errorf("%w: sentinel must already exist outside the package folder", ErrSentinelUnusable)
	}
	for _, sub := range []string{"work", "home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootPath, sub), 0700); err != nil {
			return RunRecord{}, err
		}
	}
	copyPath, copyHash, err := copyExeNoFollow(opts.ExePath, rootPath)
	if err != nil {
		return RunRecord{}, err
	}
	exeHash := copyHash
	_ = opts.ExeHash
	ident := profileIdentity{
		RunID: runID, Root: rootPath, Dev: rootID.volume, Ino: rootID.index,
		Sentinel: kSent, SentinelDev: sentID.volume, SentinelIno: sentID.index,
		ProfileName: prof.name, PackageSID: prof.sidStr, CapabilityCount: 0,
	}
	rec = RunRecord{
		RunID: runID, Root: rootPath, Work: filepath.Join(rootPath, "work"),
		Home: filepath.Join(rootPath, "home"), Tmp: filepath.Join(rootPath, "tmp"),
		XDGConfigHome: filepath.Join(rootPath, "home"), XDGDataHome: filepath.Join(rootPath, "home"),
		XDGCacheHome: filepath.Join(rootPath, "home"), XDGStateHome: filepath.Join(rootPath, "home"),
		Dev: rootID.volume, Ino: rootID.index, Nonce: nonce,
		ProfileHash: profileHash(ident), ExeSHA256: exeHash,
		Sentinel: kSent, SentinelDev: sentID.volume, SentinelIno: sentID.index,
		CreatedUnix: time.Now().Unix(), ProfileName: prof.name, PackageSID: prof.sidStr,
		ExePath: copyPath,
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return RunRecord{}, err
	}
	if err := os.WriteFile(filepath.Join(rootPath, markerName), append(body, '\n'), 0600); err != nil {
		return RunRecord{}, err
	}
	regDir := opts.RegistryDir
	if regDir == "" {
		regDir = DefaultRegistryDir()
	}
	if err := os.MkdirAll(regDir, 0700); err != nil {
		return RunRecord{}, err
	}
	regPath, regID, err := statNoFollow(regDir)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: registry: %v", ErrRefused, err)
	}
	if withinPath(rootPath, regPath) || equalFoldPath(rootPath, regPath) || (regID.volume == rootID.volume && regID.index == rootID.index) {
		return RunRecord{}, fmt.Errorf("%w: registry must stay outside the package folder", ErrRefused)
	}
	rec.RegistryDir = regDir
	rec.RegistryPath = filepath.Join(regPath, runID+".json")
	rec.MarkerPath = filepath.Join(rootPath, markerName)
	if err := writeJSON(rec.RegistryPath, rec, 0600); err != nil {
		return RunRecord{}, err
	}
	failed = false
	return rec, nil
}

func copyExeNoFollow(src, packageRoot string) (string, string, error) {
	if strings.TrimSpace(src) == "" {
		return "", "", fmt.Errorf("BLOCKED: missing executable path")
	}
	h, err := openMeta(src, windows.FILE_GENERIC_READ)
	if err != nil {
		return "", "", fmt.Errorf("BLOCKED: cannot read executable without following a reparse point: %v", err)
	}
	defer windows.CloseHandle(h)
	id, err := fileID(h)
	if err != nil || id.reparse || id.dir {
		return "", "", fmt.Errorf("BLOCKED: executable is a directory or reparse point")
	}
	dup, err := duplicate(h)
	if err != nil {
		return "", "", err
	}
	f := os.NewFile(uintptr(dup), "exe")
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(body)
	dest := filepath.Join(packageRoot, "agentpulse-launch.exe")
	p, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return "", "", err
	}
	out, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return "", "", fmt.Errorf("BLOCKED: cannot create this-run executable copy: %v", err)
	}
	defer windows.CloseHandle(out)
	var wrote uint32
	if err := windows.WriteFile(out, body, &wrote, nil); err != nil || int(wrote) != len(body) {
		return "", "", fmt.Errorf("BLOCKED: short executable copy: %v", err)
	}
	return dest, hex.EncodeToString(sum[:]), nil
}

func equalFoldPath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func underAny(path, base string) bool {
	if strings.TrimSpace(base) == "" {
		return false
	}
	return withinPath(base, path) || equalFoldPath(base, path)
}

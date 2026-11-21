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
	if opts.Ownership == nil {
		opts.Ownership = &CreationRecord{}
	}
	if err := bindSentinel(*opts.Ownership, kSent, sentID.volume, sentID.index); err != nil {
		return RunRecord{}, err
	}
	if opts.Ownership.Created {
		owned, err := openMeta(opts.Ownership.Path, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES)
		if err != nil {
			return RunRecord{}, fmt.Errorf("%w: creation record cannot be reopened", ErrSentinelUnusable)
		}
		gotID, idErr := fileID(owned)
		gotPath, pathErr := handlePath(owned)
		nonceBody, nonceErr := readRelative(owned, ".agentpulse-owned")
		windows.CloseHandle(owned)
		if idErr != nil || pathErr != nil || nonceErr != nil || gotID.reparse || !gotID.dir || gotID.volume != opts.Ownership.Volume || gotID.index != opts.Ownership.FileIndex || canonicalWinPath(gotPath) != opts.Ownership.CanonicalRoot || string(nonceBody) != opts.Ownership.Nonce {
			return RunRecord{}, fmt.Errorf("%w: creation record does not match the open fixture directory", ErrSentinelUnusable)
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
	regDir := opts.RegistryDir
	if regDir == "" {
		regDir = DefaultRegistryDir()
	}
	if err := os.MkdirAll(regDir, 0700); err != nil {
		return RunRecord{}, err
	}
	manifest := RunManifest{
		RunID: runID, Nonce: nonce, Phase: "creating", SourceSHA256: opts.ExeHash,
	}
	hooks := profileEvidence{
		Persist: func(name string) error {
			manifest.ProfileName = name
			manifest.ProfileCreated = true
			manifest.Phase = "s_ok"
			return saveManifest(regDir, manifest)
		},
		Note: func(phase string) error {
			manifest.Phase = phase
			return saveManifest(regDir, manifest)
		},
	}
	prof, err := createAppProfile(runID, hooks)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	manifest.PackageSID = prof.sidStr
	if nerr := hooks.Note("profile_created"); nerr != nil {
		freeSID(prof.sid)
		return RunRecord{}, failCreatedProfile(prof.name, nerr, deleteAppProfile, hooks.Note)
	}
	failed := true
	defer func() {
		freeSID(prof.sid)
		if failed {
			if derr := deleteAppProfile(prof.name); derr != nil {
				manifest.Phase = "delete_failed"
				serr := saveManifest(regDir, manifest)
				mpath, _ := manifestPath(regDir, runID)
				if serr != nil {
					err = fmt.Errorf("%v; profile delete failed: %w; manifest update failed: %v; profile=%s sid=%s path=%s", err, derr, serr, prof.name, prof.sidStr, mpath)
				} else {
					err = fmt.Errorf("%v; profile delete failed: %w; manifest profile=%s sid=%s path=%s", err, derr, prof.name, prof.sidStr, mpath)
				}
			} else if mpath, merr := manifestPath(regDir, runID); merr != nil {
				err = fmt.Errorf("%v; manifest path: %v", err, merr)
			} else if rerr := os.Remove(mpath); rerr != nil && !os.IsNotExist(rerr) {
				err = fmt.Errorf("%v; manifest remove failed: %v", err, rerr)
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
	if withinCanonical(rootPath, kSent) || canonicalWinPath(rootPath) == canonicalWinPath(kSent) {
		return RunRecord{}, fmt.Errorf("%w: sentinel must already exist outside the package folder", ErrSentinelUnusable)
	}
	manifest.Root = rootPath
	manifest.RootDev = rootID.volume
	manifest.RootIno = rootID.index
	manifest.Phase = "root_observed"
	if err := saveManifest(regDir, manifest); err != nil {
		return RunRecord{}, err
	}
	rootHandle, err := openMeta(rootPath, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: package folder handle: %v", ErrRefused, err)
	}
	heldID, heldErr := fileID(rootHandle)
	if heldErr != nil || heldID.reparse || !heldID.dir || heldID.volume != rootID.volume || heldID.index != rootID.index {
		windows.CloseHandle(rootHandle)
		return RunRecord{}, fmt.Errorf("%w: package folder handle is not the folder just observed", ErrRefused)
	}
	for _, sub := range []string{"work", "home", "tmp"} {
		if err := mkdirRelative(rootHandle, sub); err != nil {
			windows.CloseHandle(rootHandle)
			return RunRecord{}, err
		}
	}
	copyPath, copyHash, err := copyExeNoFollow(opts.ExePath, rootHandle, rootPath, opts.ExeHash)
	if err != nil {
		windows.CloseHandle(rootHandle)
		return RunRecord{}, err
	}
	exeHash := copyHash
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
		windows.CloseHandle(rootHandle)
		return RunRecord{}, err
	}
	if err := writeRelativeNew(rootHandle, markerName, append(body, '\n')); err != nil {
		windows.CloseHandle(rootHandle)
		return RunRecord{}, err
	}
	still, stillErr := fileID(rootHandle)
	windows.CloseHandle(rootHandle)
	if stillErr != nil || still.volume != rootID.volume || still.index != rootID.index || still.reparse {
		return RunRecord{}, fmt.Errorf("%w: package folder changed while creating the executable copy", ErrRefused)
	}
	regPath, regID, err := statNoFollow(regDir)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: registry: %v", ErrRefused, err)
	}
	if pathsConflate(rootPath, regPath) || (regID.volume == rootID.volume && regID.index == rootID.index) {
		return RunRecord{}, fmt.Errorf("%w: registry must stay outside the package folder", ErrRefused)
	}
	rec.RegistryDir = regDir
	rec.RegistryPath = filepath.Join(regPath, runID+".json")
	rec.MarkerPath = filepath.Join(rootPath, markerName)
	if err := writeJSON(rec.RegistryPath, rec, 0600); err != nil {
		return RunRecord{}, err
	}
	manifest.Root = rootPath
	manifest.RootDev = rootID.volume
	manifest.RootIno = rootID.index
	manifest.CopySHA256 = copyHash
	manifest.Phase = "root_ready"
	if err := saveManifest(regDir, manifest); err != nil {
		return RunRecord{}, err
	}
	failed = false
	return rec, nil
}

func copyExeNoFollow(src string, root windows.Handle, rootPath, expected string) (string, string, error) {
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
	sourceHash := hex.EncodeToString(sum[:])
	if err := agreeHashes(expected, sourceHash); err != nil {
		return "", "", err
	}
	out, err := ntCreate(root, "agentpulse-launch.exe", windows.GENERIC_WRITE|windows.SYNCHRONIZE, fileCreate, fileNonDirectoryFile|fileSynchronousNonAlert, 0)
	if err != nil {
		return "", "", fmt.Errorf("BLOCKED: cannot create this-run executable copy: %v", err)
	}
	var wrote uint32
	werr := windows.WriteFile(out, body, &wrote, nil)
	windows.CloseHandle(out)
	if werr != nil || int(wrote) != len(body) {
		return "", "", fmt.Errorf("BLOCKED: short executable copy: %v", werr)
	}
	back, err := readRelative(root, "agentpulse-launch.exe")
	if err != nil {
		return "", "", fmt.Errorf("BLOCKED: cannot read back the executable copy: %v", err)
	}
	again := sha256.Sum256(back)
	copyHash := hex.EncodeToString(again[:])
	if err := agreeHashes(sourceHash, copyHash); err != nil {
		return "", "", err
	}
	return copyDest(rootPath), copyHash, nil
}

func writeRelativeNew(parent windows.Handle, name string, body []byte) error {
	h, err := ntCreate(parent, name, windows.GENERIC_WRITE|windows.SYNCHRONIZE, fileCreate, fileNonDirectoryFile|fileSynchronousNonAlert, 0)
	if err != nil {
		return err
	}
	var wrote uint32
	werr := windows.WriteFile(h, body, &wrote, nil)
	windows.CloseHandle(h)
	if werr != nil || int(wrote) != len(body) {
		return fmt.Errorf("BLOCKED: short relative write: %v", werr)
	}
	return nil
}

func copyDest(rootPath string) string {
	dest := rootPath
	if !strings.HasSuffix(dest, `\`) {
		dest += `\`
	}
	return dest + "agentpulse-launch.exe"
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

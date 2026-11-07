//go:build windows

package safety

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// deleteOwned removes the recorded package folder only while the open handle
// still has the creation file index. Reparse points are not traversed.
// A missing marker or identity mismatch keeps the directory (BLOCKED).
func deleteOwned(rec RunRecord, homes []string) (CleanupOutcome, error) {
	out := CleanupOutcome{Result: "BLOCKED"}
	if err := pathShapeError(rec.Root); err != nil {
		out.Reason = err.Error()
		return out, err
	}
	if protectedPathComponent(rec.Root) {
		out.Reason = "recorded root names a protected user directory; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	h, err := openMeta(rec.Root, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|deleteAccess)
	if err != nil {
		if os.IsNotExist(err) {
			if man, merr := loadManifest(rec.RegistryDir, rec.RunID); merr == nil && man.ProfileCreated && man.Nonce == rec.Nonce && man.ProfileName == rec.ProfileName && man.ProfileName != "" {
				return recoverProfile(rec.RegistryDir, man)
			}
		}
		if os.IsNotExist(err) {
			out.Reason = "root already gone and no matching owned profile manifest"
		} else {
			out.Reason = "open failed; root retained: " + err.Error()
		}
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	closed := false
	defer func() {
		if !closed {
			windows.CloseHandle(h)
		}
	}()
	id, err := fileID(h)
	if err != nil {
		out.Reason = "file identity unreadable; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	if id.reparse || !id.dir || id.volume != rec.Dev || id.index != rec.Ino || rec.Dev == 0 && rec.Ino == 0 {
		out.Reason = "directory identity or reparse mismatch; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	kpath, err := handlePath(h)
	if err != nil || canonicalWinPath(kpath) != canonicalWinPath(rec.Root) {
		out.Reason = "kernel path mismatch; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	for _, home := range homes {
		if strings.TrimSpace(home) == "" {
			continue
		}
		if pathsConflate(home, kpath) || pathsConflate(kpath, home) {
			out.Reason = "recorded root is the protected home or its parent; root retained"
			return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
		}
		if hp, hid, herr := statNoFollow(home); herr == nil && hid.volume == id.volume && hid.index == id.index {
			out.Reason = "recorded root has the protected home file identity; root retained"
			_ = hp
			return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
		}
	}
	marker, err := readMarkerHandle(h)
	if err != nil || marker.RunID != rec.RunID || marker.Nonce != rec.Nonce || marker.Dev != rec.Dev || marker.Ino != rec.Ino {
		out.Reason = "marker missing or mismatched; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	if err := deleteChildrenHandle(h, 0); err != nil {
		out.Reason = "delete aborted; root retained: " + err.Error()
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	id2, err := fileID(h)
	if err != nil || id2.volume != rec.Dev || id2.index != rec.Ino || id2.reparse {
		out.Reason = "directory changed while deleting children; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	flag := byte(1)
	if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &flag, 1); err != nil {
		out.Reason = "disposition delete failed; root retained: " + err.Error()
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	windows.CloseHandle(h)
	closed = true
	gone, goneErr := rootIsGone(kpath, rec.Dev, rec.Ino)
	var profileErr error
	if rec.ProfileName != "" && gone {
		profileErr = deleteAppProfile(rec.ProfileName)
	}
	final, ferr := finalizeDelete(gone && goneErr == nil, profileErr)
	if goneErr != nil && ferr == nil {
		ferr = goneErr
		final.Result = "BLOCKED"
		final.Reason = goneErr.Error()
	}
	if ferr != nil {
		if man, merr := loadManifest(rec.RegistryDir, rec.RunID); merr == nil {
			if gone {
				man.Phase = "root_deleted"
			} else {
				man.Phase = "delete_failed"
			}
			_ = saveManifest(rec.RegistryDir, man)
		}
		out.Result = "BLOCKED"
		out.Reason = final.Reason
		return out, ferr
	}
	if rec.RegistryPath != "" {
		if err := os.Remove(rec.RegistryPath); err != nil && !os.IsNotExist(err) {
			out.Result = "BLOCKED"
			out.Reason = "registry remove failed after delete: " + err.Error()
			return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
		}
	}
	if err := os.Remove(manifestPath(rec.RegistryDir, rec.RunID)); err != nil && !os.IsNotExist(err) {
		out.Result = "BLOCKED"
		out.Reason = "manifest remove failed after delete: " + err.Error()
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	out.Result = "deleted"
	out.Targets = []string{rec.Root}
	return out, nil
}

func rootIsGone(path string, volume, index uint64) (bool, error) {
	_, id, err := statNoFollow(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	if id.volume == volume && id.index == index {
		return false, nil
	}
	return false, fmt.Errorf("path was replaced; replacement retained")
}

func readMarkerHandle(dir windows.Handle) (RunRecord, error) {
	h, err := openRelative(dir, markerName, windows.FILE_GENERIC_READ|deleteAccess)
	if err != nil {
		return RunRecord{}, err
	}
	defer windows.CloseHandle(h)
	id, err := fileID(h)
	if err != nil || id.reparse || id.dir {
		return RunRecord{}, fmt.Errorf("marker is not a regular file")
	}
	dup, err := duplicate(h)
	if err != nil {
		return RunRecord{}, err
	}
	f := os.NewFile(uintptr(dup), "marker")
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return RunRecord{}, err
	}
	var rec RunRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return RunRecord{}, err
	}
	return rec, nil
}

func deleteChildrenHandle(dir windows.Handle, depth int) error {
	if depth > 64 {
		return fmt.Errorf("directory depth exceeded")
	}
	names, err := listHandleNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		child, err := openRelative(dir, name, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|deleteAccess)
		if err != nil {
			return err
		}
		id, err := fileID(child)
		if err != nil {
			windows.CloseHandle(child)
			return err
		}
		if id.reparse {
			err = dispositionHandle(child)
			windows.CloseHandle(child)
			if err != nil {
				return err
			}
			continue
		}
		if id.dir {
			err = deleteChildrenHandle(child, depth+1)
			if err == nil {
				id2, ferr := fileID(child)
				if ferr != nil || id2.reparse || id2.volume != id.volume || id2.index != id.index {
					err = fmt.Errorf("child directory changed during delete")
				} else {
					err = dispositionHandle(child)
				}
			}
			windows.CloseHandle(child)
			if err != nil {
				return err
			}
			continue
		}
		err = dispositionHandle(child)
		windows.CloseHandle(child)
		if err != nil {
			return err
		}
	}
	return nil
}

func dispositionHandle(h windows.Handle) error {
	flag := byte(1)
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &flag, 1)
}

func duplicate(h windows.Handle) (windows.Handle, error) {
	var out windows.Handle
	err := windows.DuplicateHandle(windows.CurrentProcess(), h, windows.CurrentProcess(), &out, 0, false, windows.DUPLICATE_SAME_ACCESS)
	return out, err
}

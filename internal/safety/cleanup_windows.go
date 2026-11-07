//go:build windows

package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if protectedComponent(rec.Root) {
		out.Reason = "recorded root names a protected user directory; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	h, err := openMeta(rec.Root, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|deleteAccess)
	if err != nil {
		out.Reason = "open failed; root retained: " + err.Error()
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	defer windows.CloseHandle(h)
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
	if err != nil || !equalFoldPath(kpath, rec.Root) {
		out.Reason = "kernel path mismatch; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	for _, home := range homes {
		if strings.TrimSpace(home) == "" {
			continue
		}
		if equalFoldPath(home, kpath) || withinPath(kpath, home) {
			out.Reason = "recorded root is the protected home or its parent; root retained"
			return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
		}
		if hp, hid, herr := statNoFollow(home); herr == nil && hid.volume == id.volume && hid.index == id.index {
			out.Reason = "recorded root has the protected home file identity; root retained"
			_ = hp
			return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
		}
	}
	marker, err := readMarkerFile(filepath.Join(kpath, markerName))
	if err != nil || marker.RunID != rec.RunID || marker.Nonce != rec.Nonce || marker.Dev != rec.Dev || marker.Ino != rec.Ino {
		out.Reason = "marker missing or mismatched; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	if err := deleteChildren(kpath, 0); err != nil {
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
	if rec.ProfileName != "" {
		if err := deleteAppProfile(rec.ProfileName); err != nil {
			out.Reason = "directory disposition requested; profile delete: " + err.Error()
		}
	}
	if rec.ExePath != "" && rec.PackageSID != "" {
		_ = revokeExeACE(rec.ExePath, rec.PackageSID)
	}
	if rec.RegistryPath != "" {
		_ = os.Remove(rec.RegistryPath)
	}
	out.Result = "deleted"
	out.Targets = []string{rec.Root}
	return out, nil
}

func readMarkerFile(path string) (RunRecord, error) {
	_, id, err := statNoFollow(path)
	if err != nil || id.reparse || id.dir {
		return RunRecord{}, fmt.Errorf("marker is not a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return RunRecord{}, err
	}
	var rec RunRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return RunRecord{}, err
	}
	return rec, nil
}

func deleteChildren(dir string, depth int) error {
	if depth > 64 {
		return fmt.Errorf("directory depth exceeded")
	}
	pattern := dir + `\*`
	p, err := windows.UTF16PtrFromString(pattern)
	if err != nil {
		return err
	}
	var data windows.Win32finddata
	find, err := windows.FindFirstFile(p, &data)
	if err != nil {
		return err
	}
	defer windows.FindClose(find)
	for {
		name := windows.UTF16ToString(data.FileName[:])
		if name != "." && name != ".." && name != "" {
			child := dir + `\` + name
			reparse := data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
			isDir := data.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
			if reparse {
				if err := dispositionPath(child); err != nil {
					return err
				}
			} else if isDir {
				if err := deleteChildren(child, depth+1); err != nil {
					return err
				}
				if err := dispositionPath(child); err != nil {
					return err
				}
			} else if err := dispositionPath(child); err != nil {
				return err
			}
		}
		if err := windows.FindNextFile(find, &data); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return nil
			}
			return err
		}
	}
}

func dispositionPath(path string) error {
	h, err := openMeta(path, deleteAccess|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	id, err := fileID(h)
	if err != nil {
		return err
	}
	if id.reparse {
		// The handle names the reparse node itself, not its target.
	}
	flag := byte(1)
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &flag, 1)
}

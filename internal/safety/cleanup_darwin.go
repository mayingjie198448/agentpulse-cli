//go:build darwin

package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// deleteOwned removes rec.Root only when the directory fd still matches the
// creation identity and the in-directory marker. A symlink, junction/mount
// replacement, missing marker, or inode swap leaves the tree in place.
func deleteOwned(rec RunRecord, homes []string) (CleanupOutcome, error) {
	out := CleanupOutcome{Result: "BLOCKED", Targets: nil}
	if err := darwinPathShapeError(rec.Root); err != nil {
		out.Reason = err.Error()
		return out, err
	}
	for _, home := range homes {
		if home == "" {
			continue
		}
		if withinPath(home, rec.Root) || rec.Root == home {
			out.Reason = "recorded root is inside a protected home"
			return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
		}
		if withinPath(rec.Root, home) {
			out.Reason = "recorded root is a parent of a protected home"
			return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
		}
	}
	fd, err := openDirNoFollow(rec.Root)
	if err != nil {
		out.Reason = "open failed; root retained: " + err.Error()
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		out.Reason = "fstat failed; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	dev, ino := ident(st)
	if dev != rec.Dev || ino != rec.Ino || rec.Dev == 0 {
		out.Reason = "directory identity mismatch; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	kpath, err := fdPath(fd)
	if err != nil || kpath != rec.Root {
		out.Reason = "kernel path mismatch; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	if err := forbidIdentity(dev, ino, homes); err != nil {
		out.Reason = err.Error()
		return out, err
	}
	marker, err := readMarkerAt(fd)
	if err != nil {
		out.Reason = "marker missing or unreadable; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	if err := validateDarwinCleanupMarker(marker, rec, dev, ino); err != nil {
		out.Reason = err.Error()
		return out, err
	}
	if err := deleteChildren(fd, 0); err != nil {
		out.Reason = "delete aborted; root retained: " + err.Error()
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	parent := parentPath(rec.Root)
	name := baseName(rec.Root)
	parentFD, err := openDirNoFollow(parent)
	if err != nil {
		out.Reason = "parent open failed; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	defer unix.Close(parentFD)
	var childSt unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &childSt, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		out.Reason = "parent lost the run directory; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	cdev, cino := ident(childSt)
	if cdev != rec.Dev || cino != rec.Ino || childSt.Mode&unix.S_IFMT == unix.S_IFLNK {
		out.Reason = "run directory was replaced; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
		out.Reason = "unlink of empty run root failed; root retained"
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	if rec.RegistryPath != "" {
		_ = os.Remove(rec.RegistryPath)
	}
	out.Result = "deleted"
	out.Targets = []string{rec.Root}
	out.Reason = ""
	return out, nil
}

func forbidIdentity(dev, ino uint64, homes []string) error {
	rootFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err == nil {
		var st unix.Stat_t
		if unix.Fstat(rootFD, &st) == nil {
			rdev, rino := ident(st)
			if rdev == dev && rino == ino {
				unix.Close(rootFD)
				return fmt.Errorf("%w: refuse filesystem root identity", ErrCleanupBlocked)
			}
		}
		unix.Close(rootFD)
	}
	for _, home := range homes {
		if strings.TrimSpace(home) == "" {
			continue
		}
		fd, err := openDirNoFollow(home)
		if err != nil {
			continue
		}
		var st unix.Stat_t
		ok := unix.Fstat(fd, &st) == nil
		unix.Close(fd)
		if !ok {
			continue
		}
		hdev, hino := ident(st)
		if hdev == dev && hino == ino {
			return fmt.Errorf("%w: refuse protected home identity", ErrCleanupBlocked)
		}
	}
	return nil
}

func readMarkerAt(dirfd int) (RunRecord, error) {
	fd, err := unix.Openat(dirfd, markerName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return RunRecord{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return RunRecord{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return RunRecord{}, fmt.Errorf("marker is not a regular file")
	}
	b, err := readFD(fd)
	if err != nil {
		return RunRecord{}, err
	}
	var rec RunRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return RunRecord{}, err
	}
	return rec, nil
}

func deleteChildren(dirfd int, depth int) error {
	if depth > 64 {
		return fmt.Errorf("directory depth exceeded")
	}
	dup, err := unix.Dup(dirfd)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(dup), "run-root")
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "." || name == ".." || name == "" {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		mode := st.Mode & unix.S_IFMT
		if mode == unix.S_IFLNK {
			// Remove the link itself. Never follow the target.
			if err := unix.Unlinkat(dirfd, name, 0); err != nil {
				return err
			}
			continue
		}
		if mode == unix.S_IFDIR {
			child, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			err = deleteChildren(child, depth+1)
			unix.Close(child)
			if err != nil {
				return err
			}
			if err := unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		if err := unix.Unlinkat(dirfd, name, 0); err != nil {
			return err
		}
	}
	return nil
}

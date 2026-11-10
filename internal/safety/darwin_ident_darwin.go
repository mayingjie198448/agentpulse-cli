//go:build darwin

package safety

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func getDarwinFileIdentity(path string) (DarwinTargetIdentity, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return DarwinTargetIdentity{}, err
	}
	dev, ino := ident(st)
	if dev == 0 || ino == 0 {
		return DarwinTargetIdentity{}, fmt.Errorf("zero file identity for %q (dev=%d ino=%d)", path, dev, ino)
	}
	return DarwinTargetIdentity{Dev: dev, Ino: ino}, nil
}

func createDarwinExclusiveSentinel(sentinelPath string, content []byte) (string, uint64, uint64, error) {
	parent := parentPath(sentinelPath)
	name := baseName(sentinelPath)
	dirFD, err := openDirNoFollow(parent)
	if err != nil {
		return "", 0, 0, err
	}
	defer unix.Close(dirFD)
	fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", 0, 0, err
	}
	defer unix.Close(fd)
	if err := writeFD(fd, content); err != nil {
		return "", 0, 0, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", 0, 0, fmt.Errorf("%w: stat exclusive sentinel: %v", ErrSentinelUnusable, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", 0, 0, fmt.Errorf("%w: exclusive sentinel is not a regular file", ErrSentinelUnusable)
	}
	dev, ino := ident(st)
	if dev == 0 || ino == 0 {
		return "", 0, 0, fmt.Errorf("%w: exclusive sentinel has zero device or inode", ErrSentinelUnusable)
	}
	canon, err := fdPath(fd)
	if err != nil {
		canon = sentinelPath
	}
	if err := registerPendingDarwinSyntheticProbe(canon, dev, ino); err != nil {
		return "", 0, 0, err
	}
	return canon, dev, ino, nil
}

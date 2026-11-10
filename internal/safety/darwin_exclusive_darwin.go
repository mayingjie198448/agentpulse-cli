//go:build darwin

package safety

import "golang.org/x/sys/unix"

// defaultDarwinExclusiveOS binds exclusive sentinel creation to the real
// Darwin FD operations. Closures do not open anything until the chain runs.
func defaultDarwinExclusiveOS() darwinExclusiveOS {
	return darwinExclusiveOS{
		OpenDirNoFollow: openDirNoFollow,
		OpenExclusive: func(dirFD int, name string) (int, error) {
			return unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		},
		Write: writeFD,
		Fstat: func(fd int) (darwinExclusiveFileID, error) {
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil {
				return darwinExclusiveFileID{}, err
			}
			dev, ino := ident(st)
			return darwinExclusiveFileID{
				Dev:       dev,
				Ino:       ino,
				IsRegular: st.Mode&unix.S_IFMT == unix.S_IFREG,
			}, nil
		},
		FdPath: fdPath,
		Close:  func(fd int) { _ = unix.Close(fd) },
	}
}

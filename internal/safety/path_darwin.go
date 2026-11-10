//go:build darwin

package safety

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

func openDirNoFollow(path string) (int, error) {
	if err := darwinPathShapeError(path); err != nil {
		return -1, err
	}
	dir, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, part, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			unix.Close(dir)
			return -1, err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			unix.Close(dir)
			return -1, fmt.Errorf("%w: symlink component %q", ErrCleanupBlocked, part)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			unix.Close(dir)
			return -1, fmt.Errorf("%w: not a directory %q", ErrCleanupBlocked, part)
		}
		next, err := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dir)
		if err != nil {
			return -1, err
		}
		dir = next
	}
	return dir, nil
}

func openFileNoFollow(path string) (int, unix.Stat_t, error) {
	if err := darwinPathShapeError(path); err != nil {
		return -1, unix.Stat_t{}, err
	}
	parent := parentPath(path)
	name := baseName(path)
	dir, err := openDirNoFollow(parent)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, unix.Stat_t{}, err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		unix.Close(fd)
		return -1, unix.Stat_t{}, fmt.Errorf("%w: sentinel is a symlink", ErrSentinelUnusable)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return -1, unix.Stat_t{}, fmt.Errorf("%w: sentinel is not a regular file", ErrSentinelUnusable)
	}
	return fd, st, nil
}

func fdPath(fd int) (string, error) {
	buf := make([]byte, 1024)
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	n := bytes.IndexByte(buf, 0)
	if n < 0 {
		n = len(buf)
	}
	p := string(buf[:n])
	if err := darwinPathShapeError(p); err != nil {
		return "", err
	}
	return p, nil
}

func readFD(fd int) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := unix.Read(fd, buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err == unix.EINTR {
			continue
		}
		if n == 0 {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

func writeFD(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func parentPath(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "/"
	}
	return path[:i]
}

func baseName(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return path
	}
	return path[i+1:]
}

func ident(st unix.Stat_t) (uint64, uint64) {
	return uint64(uint32(st.Dev)), st.Ino
}

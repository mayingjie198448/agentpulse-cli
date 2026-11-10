package safety

import (
	"fmt"
	"strings"
	"sync"
)

// darwinExclusiveFileID is the identity read from an already-open exclusive FD.
// IsRegular is the type bit from that same Fstat, not from a path lookup.
type darwinExclusiveFileID struct {
	Dev       uint64
	Ino       uint64
	IsRegular bool
}

// darwinExclusiveOS is the only OS boundary the exclusive sentinel creation
// chain may use. Production Darwin binds it to open/fstat/F_GETPATH. Tests
// bind an in-memory fake. A caller path string is never a substitute for FdPath.
type darwinExclusiveOS struct {
	OpenDirNoFollow func(parent string) (int, error)
	OpenExclusive   func(dirFD int, name string) (int, error)
	Write           func(fd int, content []byte) error
	Fstat           func(fd int) (darwinExclusiveFileID, error)
	FdPath          func(fd int) (string, error)
	Close           func(fd int)
}

var (
	darwinExclusiveOSMu       sync.Mutex
	darwinExclusiveOSOverride *darwinExclusiveOS
)

func currentDarwinExclusiveOS() darwinExclusiveOS {
	darwinExclusiveOSMu.Lock()
	defer darwinExclusiveOSMu.Unlock()
	if darwinExclusiveOSOverride != nil {
		return *darwinExclusiveOSOverride
	}
	return defaultDarwinExclusiveOS()
}

// setDarwinExclusiveOS installs a process-local OS boundary and returns a
// restore func. It does not open files. Production leaves the override unset.
func setDarwinExclusiveOS(os darwinExclusiveOS) func() {
	darwinExclusiveOSMu.Lock()
	defer darwinExclusiveOSMu.Unlock()
	prev := darwinExclusiveOSOverride
	next := os
	darwinExclusiveOSOverride = &next
	return func() {
		darwinExclusiveOSMu.Lock()
		defer darwinExclusiveOSMu.Unlock()
		darwinExclusiveOSOverride = prev
	}
}

func exclusiveOSReady(os darwinExclusiveOS) error {
	if os.OpenDirNoFollow == nil || os.OpenExclusive == nil || os.Write == nil || os.Fstat == nil || os.FdPath == nil || os.Close == nil {
		return fmt.Errorf("%w: exclusive create OS boundary is incomplete", ErrSentinelUnusable)
	}
	return nil
}

// splitDarwinExclusiveName splits a caller path into the directory that will
// be opened and the final component passed to openat. The joined string is
// not a kernel path and must not be registered.
func splitDarwinExclusiveName(sentinelPath string) (parent, name string, err error) {
	if err := darwinPathShapeError(sentinelPath); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrSentinelUnusable, err)
	}
	i := strings.LastIndex(sentinelPath, "/")
	if i <= 0 || i == len(sentinelPath)-1 {
		return "", "", fmt.Errorf("%w: exclusive sentinel path has no parent or name", ErrSentinelUnusable)
	}
	return sentinelPath[:i], sentinelPath[i+1:], nil
}

// createExclusiveSentinelFD is the single exclusive-create proof chain.
// Both createSyntheticSentinel and createDarwinExclusiveSentinel call it.
// Pending registration happens only after Fstat and F_GETPATH succeed on the
// still-open FD. F_GETPATH failure, a non-regular type, or a zero identity
// returns before register.
func createExclusiveSentinelFD(os darwinExclusiveOS, parent, name string, content []byte) (string, uint64, uint64, error) {
	if err := exclusiveOSReady(os); err != nil {
		return "", 0, 0, err
	}
	if strings.TrimSpace(parent) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return "", 0, 0, fmt.Errorf("%w: exclusive sentinel parent or name is empty", ErrSentinelUnusable)
	}
	dirFD, err := os.OpenDirNoFollow(parent)
	if err != nil {
		return "", 0, 0, err
	}
	defer os.Close(dirFD)
	fd, err := os.OpenExclusive(dirFD, name)
	if err != nil {
		return "", 0, 0, err
	}
	defer os.Close(fd)
	if err := os.Write(fd, content); err != nil {
		return "", 0, 0, err
	}
	id, err := os.Fstat(fd)
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: stat exclusive sentinel: %v", ErrSentinelUnusable, err)
	}
	if !id.IsRegular {
		return "", 0, 0, fmt.Errorf("%w: exclusive sentinel is not a regular file", ErrSentinelUnusable)
	}
	if id.Dev == 0 || id.Ino == 0 {
		return "", 0, 0, fmt.Errorf("%w: exclusive sentinel has zero device or inode", ErrSentinelUnusable)
	}
	canon, err := os.FdPath(fd)
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: F_GETPATH failed: %v", ErrSentinelUnusable, err)
	}
	if strings.TrimSpace(canon) == "" {
		return "", 0, 0, fmt.Errorf("%w: F_GETPATH returned an empty path", ErrSentinelUnusable)
	}
	if err := registerPendingDarwinSyntheticProbe(canon, id.Dev, id.Ino); err != nil {
		return "", 0, 0, err
	}
	return canon, id.Dev, id.Ino, nil
}

// createDarwinExclusiveSentinel opens parent/name through the exclusive OS
// boundary and registers only the FD identity. On non-Darwin the default
// boundary refuses and does not echo the caller path.
func createDarwinExclusiveSentinel(sentinelPath string, content []byte) (string, uint64, uint64, error) {
	parent, name, err := splitDarwinExclusiveName(sentinelPath)
	if err != nil {
		return "", 0, 0, err
	}
	return createExclusiveSentinelFD(currentDarwinExclusiveOS(), parent, name, content)
}

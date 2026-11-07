//go:build linux

package safety

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func landlockMask() (abi int, access, scoped uint64, err error) {
	r1, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 {
		return 0, 0, 0, errno
	}
	abi = int(r1)
	if abi < 3 {
		return abi, 0, 0, fmt.Errorf("landlock abi %d has no truncate right", abi)
	}
	access = unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_REFER |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE
	if abi >= 6 {
		scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
	}
	return abi, access, scoped, nil
}

// applyLandlock confines every thread in this process so writes, renames,
// and truncates are possible only under root. The caller must be the
// isolate helper, never the long-lived agentpulse parent.
func applyLandlock(root string) error {
	_, access, scoped, err := landlockMask()
	if err != nil {
		return err
	}
	attr := unix.LandlockRulesetAttr{Handled_access_fs: access, Scoped: scoped}
	ruleset, err := createRuleset(&attr)
	if err != nil {
		return fmt.Errorf("landlock create ruleset: %w", err)
	}
	defer unix.Close(ruleset)

	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("landlock open root: %w", err)
	}
	defer unix.Close(rootFD)
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(rootFD)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), uintptr(unix.LANDLOCK_RULE_PATH_BENEATH), uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("landlock add rule: %w", errno)
	}
	// landlock_restrict_self requires no_new_privs unless CAP_SYS_ADMIN is effective.
	if _, _, errno = syscall.AllThreadsSyscall(syscall.SYS_PRCTL, unix.PR_SET_NO_NEW_PRIVS, 1, 0); errno != 0 {
		return fmt.Errorf("no_new_privs: %w", errno)
	}
	if _, _, errno = syscall.AllThreadsSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("landlock restrict: %w", errno)
	}
	return nil
}

// createRuleset keeps the write and scope masks. A shorter size is only used
// when the running kernel rejects unknown trailing zero fields. The handled
// access and scoped bits are not cleared.
func createRuleset(attr *unix.LandlockRulesetAttr) (int, error) {
	sizes := []uintptr{unsafe.Sizeof(*attr), 24}
	var last error
	for _, sz := range sizes {
		fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(attr)), sz, 0)
		if errno == 0 {
			return int(fd), nil
		}
		last = errno
		if errno != unix.EINVAL && errno != unix.E2BIG {
			return -1, errno
		}
	}
	return -1, last
}

type landlockBackend struct{}

func (landlockBackend) Name() string { return "linux-landlock" }

func (landlockBackend) Available() error {
	_, _, _, err := landlockMask()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	return nil
}

func platformBackend() Backend { return landlockBackend{} }

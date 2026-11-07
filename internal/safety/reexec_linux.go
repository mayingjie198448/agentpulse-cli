//go:build linux

package safety

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func verifyAndProbe(cfg reexecCfg) error {
	self, err := exeSHA256()
	if err != nil || self == "" || self != cfg.exeHash {
		return fmt.Errorf("exe_mismatch")
	}
	rootFD, err := openDirNoFollow(cfg.runRoot)
	if err != nil {
		return fmt.Errorf("root_identity")
	}
	defer unix.Close(rootFD)
	var st unix.Stat_t
	if err := unix.Fstat(rootFD, &st); err != nil {
		return fmt.Errorf("root_identity")
	}
	dev, ino := ident(st)
	kpath, err := fdPath(rootFD)
	if err != nil || dev != cfg.rootDev || ino != cfg.rootIno || kpath != cfg.runRoot {
		return fmt.Errorf("root_identity")
	}
	sentFD, sentSt, err := openFileNoFollow(cfg.sentinel)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("sentinel_enoent")
		}
		return fmt.Errorf("sentinel_identity")
	}
	sdev, sino := ident(sentSt)
	spath, serr := fdPath(sentFD)
	before, rerr := readFD(sentFD)
	unix.Close(sentFD)
	if serr != nil || rerr != nil || sdev != cfg.sentinelDev || sino != cfg.sentinelIno || spath != cfg.sentinel {
		return fmt.Errorf("sentinel_identity")
	}
	if withinPath(cfg.runRoot, spath) {
		return fmt.Errorf("sentinel_identity")
	}
	abi, access, scoped, err := landlockMask()
	if err != nil {
		return fmt.Errorf("landlock_failed")
	}
	got := profileHash(profileIdentity{
		RunID: cfg.runID, ABI: abi, AccessFS: access, Scoped: scoped,
		Root: kpath, Dev: dev, Ino: ino,
		Sentinel: spath, SentinelDev: sdev, SentinelIno: sino,
	})
	if got != cfg.profileHash {
		return fmt.Errorf("profile_mismatch")
	}
	if err := applyLandlock(kpath); err != nil {
		// Keep the ENOTSUP token visible to the parent. The user command is
		// still not executed: failVerify runs before confirmAndExec.
		if errors.Is(err, errNoNewPrivsAllThreadsUnsupported) {
			return fmt.Errorf("landlock_failed: %w", errNoNewPrivsAllThreadsUnsupported)
		}
		return fmt.Errorf("landlock_failed")
	}
	if err := probeSentinel(cfg.sentinel, before); err != nil {
		return err
	}
	probe := cfg.workRoot + "/probe-ok"
	if err := os.WriteFile(probe, []byte("probe-ok\n"), 0600); err != nil {
		return fmt.Errorf("work_write_failed")
	}
	return nil
}

func probeSentinel(path string, before []byte) error {
	err := os.WriteFile(path, []byte("hacked-by-probe\n"), 0600)
	if err == nil {
		return fmt.Errorf("sentinel_not_denied")
	}
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("sentinel_enoent")
	}
	if !isDenied(err) {
		return fmt.Errorf("sentinel_not_denied")
	}
	if err := os.Remove(path); err == nil || os.IsNotExist(err) {
		return fmt.Errorf("sentinel_not_denied")
	}
	if !isDenied(err) && !os.IsNotExist(err) {
		return fmt.Errorf("sentinel_not_denied")
	}
	sibling := path + ".child-created"
	if err := os.WriteFile(sibling, []byte("x"), 0600); err == nil {
		return fmt.Errorf("sentinel_not_denied")
	}
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("sentinel_enoent")
	}
	if !isDenied(err) {
		return fmt.Errorf("sentinel_not_denied")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("sentinel_enoent")
		}
		return fmt.Errorf("sentinel_identity")
	}
	if string(after) != string(before) {
		return fmt.Errorf("sentinel_changed")
	}
	return nil
}

func isDenied(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || os.IsPermission(err)
}

func failVerify(confirm int, reason string) {
	if reason == "" {
		reason = "verify_failed"
	}
	if confirm >= 0 {
		_, _ = unix.Write(confirm, []byte("FAIL "+reason+"\n"))
		_ = unix.Close(confirm)
	}
	os.Exit(HelperExitVerify)
}

func confirmAndExec(cfg reexecCfg) error {
	_, _ = unix.Write(3, []byte("OK\n"))
	_ = unix.Close(3)
	return unix.Exec(cfg.user[0], cfg.user, os.Environ())
}

func closeExtraFDs(keep int) error {
	f, err := os.Open("/proc/self/fd")
	if err != nil {
		return err
	}
	dirFD := int(f.Fd())
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		n, err := strconv.Atoi(name)
		if err != nil || n <= 2 || n == keep || n == dirFD {
			continue
		}
		_ = unix.Close(n)
	}
	return nil
}

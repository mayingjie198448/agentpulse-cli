//go:build darwin

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
	prof := darwinProfileIdentity(cfg.runID, kpath, spath, dev, ino, sdev, sino)
	got := profileHash(prof)
	if got != cfg.profileHash {
		return fmt.Errorf("profile_mismatch")
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
	writeErr := os.WriteFile(path, []byte("hacked-by-probe\n"), 0600)
	if !isDenied(writeErr) {
		if os.IsNotExist(writeErr) || errors.Is(writeErr, syscall.ENOENT) {
			return fmt.Errorf("sentinel_enoent")
		}
		return fmt.Errorf("sentinel_not_denied")
	}
	removeErr := os.Remove(path)
	if !isDenied(removeErr) {
		if os.IsNotExist(removeErr) {
			return fmt.Errorf("sentinel_enoent")
		}
		return fmt.Errorf("sentinel_not_denied")
	}
	sibling := path + ".child-created"
	createErr := os.WriteFile(sibling, []byte("x"), 0600)
	if !isDenied(createErr) {
		if os.IsNotExist(createErr) || errors.Is(createErr, syscall.ENOENT) {
			return fmt.Errorf("sentinel_enoent")
		}
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
	if err == nil {
		return false
	}
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
	return darwinConfirmAndExec(3, cfg.user, os.Environ(), darwinConfirmExecSeam{
		Write: unix.Write,
		Close: unix.Close,
		Exec:  unix.Exec,
	})
}

func closeExtraFDs(keep int) error {
	f, err := os.Open("/dev/fd")
	if err == nil {
		dirFD := int(f.Fd())
		names, err := f.Readdirnames(-1)
		f.Close()
		if err == nil {
			for _, name := range names {
				n, err := strconv.Atoi(name)
				if err != nil || n <= 2 || n == keep || n == dirFD {
					continue
				}
				_ = unix.Close(n)
			}
			return nil
		}
	}
	for fd := 4; fd < 64; fd++ {
		if fd != keep {
			_ = unix.Close(fd)
		}
	}
	return nil
}

//go:build windows

package safety

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func verifyAndProbe(cfg reexecCfg) error {
	self, err := exeSHA256()
	if err != nil || self == "" || self != cfg.exeHash {
		return fmt.Errorf("exe_mismatch")
	}
	rootPath, rootID, err := statNoFollow(cfg.runRoot)
	if err != nil || rootID.reparse || !rootID.dir || rootID.volume != cfg.rootDev || rootID.index != cfg.rootIno || !equalFoldPath(rootPath, cfg.runRoot) {
		return fmt.Errorf("root_identity")
	}
	sentPath, sentID, err := statNoFollow(cfg.sentinel)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("sentinel_enoent")
		}
		return fmt.Errorf("sentinel_identity")
	}
	if sentID.dir || sentID.reparse || sentID.volume != cfg.sentinelDev || sentID.index != cfg.sentinelIno || !equalFoldPath(sentPath, cfg.sentinel) {
		return fmt.Errorf("sentinel_identity")
	}
	if withinPath(rootPath, sentPath) || equalFoldPath(rootPath, sentPath) {
		return fmt.Errorf("sentinel_identity")
	}
	before, err := os.ReadFile(cfg.sentinel)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("sentinel_enoent")
		}
		return fmt.Errorf("sentinel_identity")
	}
	got := profileHash(profileIdentity{
		RunID: cfg.runID, Root: rootPath, Dev: rootID.volume, Ino: rootID.index,
		Sentinel: sentPath, SentinelDev: sentID.volume, SentinelIno: sentID.index,
		ProfileName: cfg.profileName, PackageSID: cfg.packageSID, CapabilityCount: cfg.capCount,
	})
	if got != cfg.profileHash {
		return fmt.Errorf("profile_mismatch")
	}
	if err := probeSentinelWindows(cfg.sentinel, before); err != nil {
		return err
	}
	if err := os.WriteFile(cfg.workRoot+`\probe-ok`, []byte("probe-ok\n"), 0600); err != nil {
		return fmt.Errorf("work_write_failed")
	}
	return nil
}

func probeSentinelWindows(path string, before []byte) error {
	err := os.WriteFile(path, []byte("hacked-by-probe\n"), 0600)
	if err == nil {
		return fmt.Errorf("sentinel_not_denied")
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("sentinel_enoent")
	}
	if !os.IsPermission(err) {
		return fmt.Errorf("sentinel_not_denied")
	}
	if err := os.Remove(path); err == nil || os.IsNotExist(err) {
		return fmt.Errorf("sentinel_not_denied")
	}
	if !os.IsPermission(err) {
		return fmt.Errorf("sentinel_not_denied")
	}
	if err := os.WriteFile(path+".child-created", []byte("x"), 0600); err == nil {
		return fmt.Errorf("sentinel_not_denied")
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("sentinel_enoent")
	}
	if !os.IsPermission(err) {
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

func closeExtraFDs(keep int) error { return nil }

func failVerify(confirm int, reason string) {
	if reason == "" {
		reason = "verify_failed"
	}
	if confirm != 0 {
		if f := os.NewFile(uintptr(confirm), "confirm"); f != nil {
			_, _ = f.Write([]byte("FAIL " + reason + "\n"))
			_ = f.Close()
		}
	}
	os.Exit(HelperExitVerify)
}

func confirmAndExec(cfg reexecCfg) error {
	if cfg.confirmHandle != 0 {
		_ = windows.SetHandleInformation(windows.Handle(cfg.confirmHandle), windows.HANDLE_FLAG_INHERIT, 0)
	}
	cmd := exec.Command(cfg.user[0], cfg.user[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = cfg.workRoot
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	if cfg.confirmHandle != 0 {
		if f := os.NewFile(uintptr(cfg.confirmHandle), "confirm"); f != nil {
			_, _ = f.Write([]byte("OK\n"))
			_ = f.Close()
		}
	}
	err := cmd.Wait()
	code, ok := exitCodeOf(err)
	if !ok {
		code = 127
	}
	os.Exit(code)
	return nil
}

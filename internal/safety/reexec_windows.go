//go:build windows

package safety

import (
	"fmt"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func verifyAndProbe(cfg reexecCfg) error {
	self, err := exeSHA256()
	if err != nil || self == "" || self != cfg.exeHash {
		return fmt.Errorf("exe_mismatch")
	}
	rootPath, rootID, err := statObserve(cfg.runRoot)
	if err != nil || rootID.reparse || !rootID.dir || rootID.volume != cfg.rootDev || rootID.index != cfg.rootIno || canonicalWinPath(rootPath) != canonicalWinPath(cfg.runRoot) {
		return fmt.Errorf("root_identity")
	}
	if !inAppContainer() {
		return fmt.Errorf("not_appcontainer")
	}
	if err := tokenPackageMatches(cfg.packageSID); err != nil {
		return err
	}
	sentPath, sentID, sentErr := statObserve(cfg.sentinel)
	observedOK := sentErr == nil
	if os.IsNotExist(sentErr) {
		return fmt.Errorf("sentinel_enoent")
	}
	if sentErr != nil && !os.IsPermission(sentErr) {
		return fmt.Errorf("sentinel_identity")
	}
	bound, err := hashSentinelBinding(observedOK,
		FileBinding{Path: sentPath, Volume: sentID.volume, Index: sentID.index},
		FileBinding{Path: cfg.sentinel, Volume: cfg.sentinelDev, Index: cfg.sentinelIno})
	if err != nil {
		return err
	}
	var before []byte
	if observedOK {
		if sentID.dir || sentID.reparse || withinCanonical(rootPath, sentPath) || canonicalWinPath(rootPath) == canonicalWinPath(sentPath) {
			return fmt.Errorf("sentinel_identity")
		}
		before, err = os.ReadFile(cfg.sentinel)
		if err != nil && !os.IsPermission(err) {
			if os.IsNotExist(err) {
				return fmt.Errorf("sentinel_enoent")
			}
			return fmt.Errorf("sentinel_identity")
		}
	}
	got := profileHash(profileIdentity{
		RunID: cfg.runID, Root: cfg.runRoot, Dev: rootID.volume, Ino: rootID.index,
		Sentinel: bound.Path, SentinelDev: bound.Volume, SentinelIno: bound.Index,
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
	writeErr := os.WriteFile(path, []byte("hacked-by-probe\n"), 0600)
	if err := requireAccessDenied(writeErr); err != nil {
		return err
	}
	removeErr := os.Remove(path)
	if err := requireAccessDenied(removeErr); err != nil {
		return err
	}
	createErr := os.WriteFile(path+".child-created", []byte("x"), 0600)
	if err := requireAccessDenied(createErr); err != nil {
		return err
	}
	if before == nil {
		return nil
	}
	after, err := os.ReadFile(path)
	if err != nil {
		if os.IsPermission(err) {
			return nil
		}
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

func statObserve(path string) (string, winFileID, error) {
	if err := pathShapeError(path); err != nil {
		return "", winFileID{}, err
	}
	h, err := openMeta(path, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return "", winFileID{}, err
	}
	defer windows.CloseHandle(h)
	id, err := fileID(h)
	if err != nil {
		return "", winFileID{}, err
	}
	if id.reparse {
		return "", id, fmt.Errorf("%w: reparse point", ErrCleanupBlocked)
	}
	kpath, err := handlePath(h)
	return kpath, id, err
}

func tokenPackageMatches(want string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("token_query_failed")
	}
	defer token.Close()
	var n uint32
	// TokenAppContainerSid = 31. A failure here is not treated as a match.
	err := windows.GetTokenInformation(token, 31, nil, 0, &n)
	if err != nil && n == 0 {
		return fmt.Errorf("token_query_failed")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(token, 31, &buf[0], uint32(len(buf)), &n); err != nil {
		return fmt.Errorf("token_query_failed")
	}
	sid := *(**windows.SID)(unsafe.Pointer(&buf[0]))
	got, err := sidString(sid)
	if err != nil || got == "" || got != want {
		return fmt.Errorf("package_sid_mismatch")
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

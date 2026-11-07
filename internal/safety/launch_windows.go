//go:build windows

package safety

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureConfinedLaunch(p *Prepared) error {
	if p == nil || !p.Confined || p.Cmd == nil || p.rec == nil {
		return nil
	}
	if p.rec.ProfileName == "" || p.rec.PackageSID == "" {
		return fmt.Errorf("BLOCKED: AppContainer profile was not created for this run")
	}
	if p.confirmW != nil {
		_ = windows.SetHandleInformation(windows.Handle(p.confirmW.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
	}
	if p.confirmR != nil {
		_ = windows.SetHandleInformation(windows.Handle(p.confirmR.Fd()), windows.HANDLE_FLAG_INHERIT, 0)
	}
	if p.confirmW != nil {
		p.Cmd.Args = insertBeforeSeparator(p.Cmd.Args, []string{"--confirm-handle", fmt.Sprintf("%d", p.confirmW.Fd())})
	}
	p.Cmd.ExtraFiles = nil
	p.audit.IsolationBackend = "windows-appcontainer"
	p.audit.IsolationDetail = "LowBox AppContainer capability_count=0; write root is the per-run package folder; no internetClient capability; no home DACL rewrite; credential broker IPC BLOCKED; cross-compile is not a native pass"
	p.audit.Network = "no_internetClient_capability"
	p.startFn = func() error { return startAppContainer(p) }
	return nil
}

func insertBeforeSeparator(args, extra []string) []string {
	for i, a := range args {
		if a == "--" {
			out := append([]string{}, args[:i]...)
			out = append(out, extra...)
			out = append(out, args[i:]...)
			return out
		}
	}
	return append(args, extra...)
}

func startAppContainer(p *Prepared) error {
	namePtr, err := windows.UTF16PtrFromString(p.rec.ProfileName)
	if err != nil {
		return err
	}
	var sid *windows.SID
	if err := deriveSID(namePtr, &sid); err != nil {
		return err
	}
	defer freeSID(sid)
	revoke, err := grantExeIfAllowed(p.Cmd.Path, p.rec.Root, sid)
	if err != nil {
		return err
	}
	if revoke != nil {
		defer revoke()
	}
	stdin, closeIn, err := inheritReader(p.Cmd.Stdin)
	if err != nil {
		return err
	}
	stdout, closeOut, err := inheritWriter(p.Cmd.Stdout)
	if err != nil {
		closeIn()
		return err
	}
	stderr, closeErr, err := inheritWriter(p.Cmd.Stderr)
	if err != nil {
		closeIn()
		closeOut()
		return err
	}
	closers := []func(){closeIn, closeOut, closeErr}

	var listSize uintptr
	_, _, _ = procInitializeProcThreadAttributeList.Call(0, 2, 0, uintptr(unsafe.Pointer(&listSize)))
	if listSize == 0 {
		return fmt.Errorf("BLOCKED: InitializeProcThreadAttributeList did not report a size")
	}
	list := make([]byte, listSize)
	r, _, callErr := procInitializeProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&list[0])), 2, 0, uintptr(unsafe.Pointer(&listSize)))
	if r == 0 {
		return fmt.Errorf("BLOCKED: InitializeProcThreadAttributeList: %v", callErr)
	}
	defer procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&list[0])))

	caps := securityCapabilities{AppContainerSid: sid, CapabilityCount: 0}
	var pinner runtime.Pinner
	pinner.Pin(sid)
	pinner.Pin(&caps)
	defer pinner.Unpin()
	r, _, callErr = procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&list[0])),
		0,
		procThreadAttributeSecurityCapabilities,
		uintptr(unsafe.Pointer(&caps)),
		unsafe.Sizeof(caps),
		0, 0,
	)
	if r == 0 {
		return fmt.Errorf("BLOCKED: UpdateProcThreadAttribute SECURITY_CAPABILITIES: %v", callErr)
	}
	handles := []windows.Handle{stdin, stdout, stderr}
	if p.confirmW != nil {
		handles = append(handles, windows.Handle(p.confirmW.Fd()))
	}
	pinner.Pin(&handles[0])
	r, _, callErr = procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&list[0])),
		0,
		0x20002, // PROC_THREAD_ATTRIBUTE_HANDLE_LIST
		uintptr(unsafe.Pointer(&handles[0])),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
		0, 0,
	)
	if r == 0 {
		for _, c := range closers {
			c()
		}
		return fmt.Errorf("BLOCKED: UpdateProcThreadAttribute HANDLE_LIST: %v", callErr)
	}

	cmdLine, err := windows.UTF16PtrFromString(quoteCommandLine(p.Cmd.Args))
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(p.Cmd.Dir)
	if err != nil {
		return err
	}
	env, envKeep, err := envBlock(p.Cmd.Env)
	if err != nil {
		return err
	}
	_ = envKeep
	app, err := windows.UTF16PtrFromString(p.Cmd.Path)
	if err != nil {
		return err
	}
	si := startupInfoEx{}
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.StartupInfo.Flags = startfUseStdHandles
	si.StartupInfo.StdInput = stdin
	si.StartupInfo.StdOutput = stdout
	si.StartupInfo.StdErr = stderr
	si.AttributeList = &list[0]
	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		app, cmdLine, nil, nil, true,
		extendedStartupInfoPresent|createUnicodeEnvironment,
		env, dir, &si.StartupInfo, &pi,
	)
	if err != nil {
		for _, c := range closers {
			c()
		}
		if err == windows.ERROR_ACCESS_DENIED {
			return fmt.Errorf("BLOCKED: AppContainer cannot start the executable (access denied). Refusing to rewrite ACLs outside the fixture temp or package folder. Missing capability: executable image readable by this AppContainer SID. Win32: %v", err)
		}
		return fmt.Errorf("BLOCKED: CreateProcess AppContainer: %v", err)
	}
	_ = windows.CloseHandle(pi.Thread)
	proc, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Process)
		for _, c := range closers {
			c()
		}
		return err
	}
	p.pid = int(pi.ProcessId)
	p.waitFn = func() error {
		defer windows.CloseHandle(pi.Process)
		for _, c := range closers {
			c()
		}
		_, err := proc.Wait()
		return err
	}
	p.killFn = func() error {
		_ = windows.TerminateProcess(pi.Process, 1)
		return nil
	}
	return nil
}

func grantExeIfAllowed(exe, packageRoot string, sid *windows.SID) (func(), error) {
	if exe == "" || sid == nil {
		return nil, nil
	}
	clean := filepath.Clean(exe)
	temp := filepath.Clean(os.TempDir())
	allowed := strings.EqualFold(clean, temp) || withinPath(temp, clean) || withinPath(packageRoot, clean)
	if !allowed || protectedComponent(clean) {
		return nil, nil
	}
	info, err := os.Stat(clean)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("BLOCKED: executable ACE target is not a file")
	}
	if err := addFileACE(clean, sid, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE, windows.GRANT_ACCESS, windows.NO_INHERITANCE); err != nil {
		return nil, fmt.Errorf("BLOCKED: cannot grant the AppContainer SID read/execute on the fixture executable only: %v", err)
	}
	return func() {
		_ = addFileACE(clean, sid, 0, windows.REVOKE_ACCESS, windows.NO_INHERITANCE)
	}, nil
}

func addFileACE(path string, sid *windows.SID, mask windows.ACCESS_MASK, mode windows.ACCESS_MODE, inherit uint32) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("NULL DACL; refusing to replace it")
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(sid)
	access := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        mode,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}
	acl, err := windows.ACLFromEntries(access, dacl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func revokeExeACE(path, sidStr string) error {
	if path == "" || sidStr == "" {
		return nil
	}
	return nil
}

func inheritReader(r io.Reader) (windows.Handle, func(), error) {
	if r == nil {
		f, err := os.Open(os.DevNull)
		if err != nil {
			return 0, func() {}, err
		}
		return windows.Handle(f.Fd()), func() { f.Close() }, markInherit(windows.Handle(f.Fd()))
	}
	if f, ok := r.(*os.File); ok {
		return windows.Handle(f.Fd()), func() {}, markInherit(windows.Handle(f.Fd()))
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return 0, func() {}, err
	}
	go func() {
		_, _ = io.Copy(pw, r)
		_ = pw.Close()
	}()
	return windows.Handle(pr.Fd()), func() { pr.Close() }, markInherit(windows.Handle(pr.Fd()))
}

func inheritWriter(w io.Writer) (windows.Handle, func(), error) {
	if w == nil {
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return 0, func() {}, err
		}
		return windows.Handle(f.Fd()), func() { f.Close() }, markInherit(windows.Handle(f.Fd()))
	}
	if f, ok := w.(*os.File); ok {
		return windows.Handle(f.Fd()), func() {}, markInherit(windows.Handle(f.Fd()))
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return 0, func() {}, err
	}
	go func() {
		_, _ = io.Copy(w, pr)
		_ = pr.Close()
	}()
	return windows.Handle(pw.Fd()), func() { pw.Close() }, markInherit(windows.Handle(pw.Fd()))
}

func markInherit(h windows.Handle) error {
	return windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
}

func envBlock(env []string) (*uint16, []uint16, error) {
	if len(env) == 0 {
		return nil, nil, nil
	}
	var block []uint16
	for _, kv := range env {
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, nil, err
		}
		block = append(block, u...)
	}
	block = append(block, 0)
	return &block[0], block, nil
}

func quoteCommandLine(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteWindowsArg(a))
	}
	return b.String()
}

func quoteWindowsArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			for ; slashes > 0; slashes-- {
				b.WriteString(`\\`)
			}
			b.WriteString(`\"`)
		default:
			for ; slashes > 0; slashes-- {
				b.WriteByte('\\')
			}
			b.WriteByte(s[i])
		}
	}
	for ; slashes > 0; slashes-- {
		b.WriteString(`\\`)
	}
	b.WriteByte('"')
	return b.String()
}

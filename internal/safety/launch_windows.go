//go:build windows

package safety

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCompareStringOrdinal = modKernel32.NewProc("CompareStringOrdinal")
)

type handleInheritSetter func(handle windows.Handle, mask, flags uint32) error

func configureConfirmInheritance(
	wHandle windows.Handle,
	hasW bool,
	rHandle windows.Handle,
	hasR bool,
	setter handleInheritSetter,
	continuation func() error,
) error {
	if setter == nil {
		setter = windows.SetHandleInformation
	}
	if hasW {
		if err := setter(wHandle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			var errno windows.Errno
			if errors.As(err, &errno) {
				return fmt.Errorf("BLOCKED: SetHandleInformation confirm pipe write handle: (errno %d) %w", errno, err)
			}
			return fmt.Errorf("BLOCKED: SetHandleInformation confirm pipe write handle: %w", err)
		}
	}
	if hasR {
		if err := setter(rHandle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			var errno windows.Errno
			if errors.As(err, &errno) {
				return fmt.Errorf("BLOCKED: SetHandleInformation confirm pipe read handle: (errno %d) %w", errno, err)
			}
			return fmt.Errorf("BLOCKED: SetHandleInformation confirm pipe read handle: %w", err)
		}
	}
	if continuation != nil {
		return continuation()
	}
	return nil
}

type launchLifetime struct {
	list       []byte
	caps       *securityCapabilities
	handles    []windows.Handle
	sid        *windows.SID
	deleteList func() error
	unpin      func()
	releaseSID func(*windows.SID) error
}

func (l *launchLifetime) Teardown() error {
	if l == nil {
		return nil
	}
	var firstErr error
	if l.deleteList != nil {
		if err := l.deleteList(); err != nil && firstErr == nil {
			firstErr = err
		}
		l.deleteList = nil
	}
	runtime.KeepAlive(l.list)
	runtime.KeepAlive(l.caps)
	runtime.KeepAlive(l.handles)
	runtime.KeepAlive(l.sid)

	if l.unpin != nil {
		l.unpin()
		l.unpin = nil
	}
	if l.releaseSID != nil && l.sid != nil {
		if err := l.releaseSID(l.sid); err != nil && firstErr == nil {
			firstErr = err
		}
		l.sid = nil
		l.releaseSID = nil
	}
	return firstErr
}

func configureConfinedLaunch(p *Prepared) error {
	if p == nil || !p.Confined || p.Cmd == nil || p.rec == nil {
		return nil
	}
	if p.rec.ProfileName == "" || p.rec.PackageSID == "" {
		return fmt.Errorf("BLOCKED: AppContainer profile was not created for this run")
	}

	var wHandle windows.Handle
	hasW := p.confirmW != nil
	if hasW {
		wHandle = windows.Handle(p.confirmW.Fd())
	}
	var rHandle windows.Handle
	hasR := p.confirmR != nil
	if hasR {
		rHandle = windows.Handle(p.confirmR.Fd())
	}

	return configureConfirmInheritance(wHandle, hasW, rHandle, hasR, windows.SetHandleInformation, func() error {
		if hasW {
			p.Cmd.Args = insertBeforeSeparator(p.Cmd.Args, []string{"--confirm-handle", fmt.Sprintf("%d", p.confirmW.Fd())})
		}
		p.Cmd.ExtraFiles = nil
		if p.rec.ExePath != "" {
			original := p.Cmd.Path
			p.Cmd.Path = p.rec.ExePath
			if len(p.Cmd.Args) > 0 {
				p.Cmd.Args[0] = p.rec.ExePath
			}
			for i, a := range p.Cmd.Args {
				if a == "--" && i+1 < len(p.Cmd.Args) && strings.EqualFold(p.Cmd.Args[i+1], original) {
					p.Cmd.Args[i+1] = p.rec.ExePath
					break
				}
			}
		}
		p.audit.IsolationBackend = "windows-appcontainer"
		p.audit.IsolationDetail = "LowBox AppContainer capability_count=0; write root is the per-run package folder; this-run executable copy only; no DACL edits; no internetClient capability; credential broker IPC BLOCKED; cross-compile is not a native pass"
		p.audit.Network = "no_internetClient_capability"
		if p.Cmd != nil {
			p.Cmd.Env = addCriticalEnv(p.Cmd.Env)
			if p.rec != nil && p.rec.Root != "" {
				hadLocalApp := hasKeyFold(p.Cmd.Env, "LOCALAPPDATA")
				replenished, err := replenishChildLocalAppData(p.Cmd.Env, p.rec)
				if err != nil {
					return err
				}
				p.Cmd.Env = replenished
				p.ChildLocalAppDataIsOwnedRoot, p.ChildLocalAppDataFromOwnedRoot = observeLocalAppDataProvenance(
					p.Cmd.Env,
					p.rec.Root,
					hadLocalApp,
				)
			} else if p.rec != nil && hasKeyFold(p.Cmd.Env, "LOCALAPPDATA") {
				p.ChildLocalAppDataIsOwnedRoot, p.ChildLocalAppDataFromOwnedRoot = observeLocalAppDataProvenance(
					p.Cmd.Env,
					p.rec.Root,
					true,
				)
			}
		}
		p.startFn = func() error { return startAppContainer(p) }
		return nil
	})
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
	lifetime := &launchLifetime{
		sid:        sid,
		releaseSID: sidFreeReleaser,
	}
	defer lifetime.Teardown()
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
	started := false
	defer func() {
		if !started {
			for _, c := range closers {
				c()
			}
		}
	}()

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
	lifetime.list = list
	lifetime.deleteList = func() error {
		procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&list[0])))
		return nil
	}

	caps := securityCapabilities{AppContainerSid: sid, CapabilityCount: 0}
	var pinner runtime.Pinner
	pinner.Pin(sid)
	pinner.Pin(&caps)
	lifetime.caps = &caps
	lifetime.unpin = pinner.Unpin
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
	lifetime.handles = handles
	r, _, callErr = procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&list[0])),
		0,
		0x20002, // PROC_THREAD_ATTRIBUTE_HANDLE_LIST
		uintptr(unsafe.Pointer(&handles[0])),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
		0, 0,
	)
	if r == 0 {
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
	p.Cmd.Env = addCriticalEnv(p.Cmd.Env)
	if !hasKeyFold(p.Cmd.Env, "LOCALAPPDATA") {
		replenished, err := replenishChildLocalAppData(p.Cmd.Env, p.rec)
		if err != nil {
			return err
		}
		p.Cmd.Env = replenished
		if p.rec != nil {
			p.ChildLocalAppDataIsOwnedRoot, p.ChildLocalAppDataFromOwnedRoot = observeLocalAppDataProvenance(
				p.Cmd.Env,
				p.rec.Root,
				false,
			)
		}
	} else if p.rec != nil && p.rec.Root != "" {
		p.ChildLocalAppDataIsOwnedRoot, p.ChildLocalAppDataFromOwnedRoot = observeLocalAppDataProvenance(
			p.Cmd.Env,
			p.rec.Root,
			true,
		)
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

	creationFlags := uint32(extendedStartupInfoPresent | createUnicodeEnvironment | windows.CREATE_SUSPENDED)
	meta := captureWindowsLaunchMetadata(
		creationFlags,
		&si,
		&caps,
		handles,
		env,
		envKeep,
		p.Cmd.Path,
		p.Cmd.Dir,
		defaultFixedKeyLookup,
	)
	p.audit.WindowsLaunchMetadata = meta

	err = windows.CreateProcess(
		app, cmdLine, nil, nil, true,
		creationFlags,
		env, dir, &si.StartupInfo, &pi,
	)
	if err != nil {
		return formatCreateProcessError(err, meta)
	}
	proc, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		if qerr := abandonLaunch(pi.Thread, pi.Process, 0); qerr != nil {
			return qerr
		}
		return err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		if qerr := abandonLaunch(pi.Thread, pi.Process, 0); qerr != nil {
			return qerr
		}
		return fmt.Errorf("BLOCKED: CreateJobObject: %v", err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		if qerr := abandonLaunch(pi.Thread, pi.Process, job); qerr != nil {
			return qerr
		}
		return fmt.Errorf("BLOCKED: job lifecycle limit: %v", err)
	}
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		if qerr := abandonLaunch(pi.Thread, pi.Process, job); qerr != nil {
			return qerr
		}
		return fmt.Errorf("BLOCKED: AssignProcessToJobObject: %v", err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		if qerr := abandonLaunch(pi.Thread, pi.Process, job); qerr != nil {
			return qerr
		}
		return fmt.Errorf("BLOCKED: ResumeThread: %v", err)
	}
	_ = windows.CloseHandle(pi.Thread)
	p.pid = int(pi.ProcessId)
	p.waitFn = func() error {
		return (launchSession{
			Cancel: waitCtx(p),
			Wait: func() (int, error) {
				state, waitErr := proc.Wait()
				code := 0
				if state != nil {
					code = state.ExitCode()
				}
				return code, waitErr
			},
			Terminate: func() error { return windows.TerminateJobObject(job, 1) },
			Active:    func() (int, error) { return jobActiveProcesses(job) },
			Release: func() {
				windows.CloseHandle(pi.Process)
				windows.CloseHandle(job)
				for _, c := range closers {
					c()
				}
			},
		}).WaitQuiescent()
	}
	p.killFn = func() error {
		return windows.TerminateJobObject(job, 1)
	}
	started = true
	return nil
}

type fixedKeyLookup func(key string) (string, bool)

func defaultFixedKeyLookup(key string) (string, bool) {
	return os.LookupEnv(key)
}

func classifyWindowsPath(p string) WindowsPathCategory {
	if p == "" {
		return PathCategoryEmpty
	}
	norm := strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(norm, `\\?\`) || strings.HasPrefix(norm, `\??\`) || strings.HasPrefix(norm, `\\.\`) {
		return PathCategoryNTDevice
	}
	if strings.HasPrefix(norm, `\\`) {
		return PathCategoryUNC
	}
	if strings.HasPrefix(norm, `\`) {
		return PathCategoryRootRelative
	}
	if len(norm) >= 2 && isDriveLetter(norm[0]) && norm[1] == ':' {
		if len(norm) >= 3 && norm[2] == '\\' {
			return PathCategoryDriveAbsolute
		}
		return PathCategoryDriveRelative
	}
	return PathCategoryRelative
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func queryFixedKey(lookup fixedKeyLookup, key string) (present bool, nonempty bool) {
	if lookup == nil {
		lookup = defaultFixedKeyLookup
	}
	var keysToTry []string
	if strings.EqualFold(key, "SYSTEMROOT") {
		keysToTry = []string{"SYSTEMROOT", "SystemRoot", "systemroot"}
	} else if strings.EqualFold(key, "LOCALAPPDATA") {
		keysToTry = []string{"LOCALAPPDATA", "LocalAppData", "localappdata"}
	} else {
		keysToTry = []string{key}
	}
	for _, k := range keysToTry {
		if val, ok := lookup(k); ok {
			return true, len(val) > 0
		}
	}
	return false, false
}

func equalFoldU16(u []uint16, ascii string) bool {
	if len(u) != len(ascii) {
		return false
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		a := uint16(ascii[i])
		if c == a {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if a >= 'a' && a <= 'z' {
			a -= 'a' - 'A'
		}
		if c != a {
			return false
		}
	}
	return true
}

func extractKeyFromU16(e []uint16) []uint16 {
	for j, c := range e {
		if c == '=' {
			if j == 0 && len(e) >= 4 && e[2] == ':' && e[3] == '=' {
				return e[:3]
			}
			return e[:j]
		}
	}
	return e
}

func inspectEnvBlock(envPtr *uint16, block []uint16) (
	provided bool,
	unitCount int,
	doubleNul bool,
	structValid bool,
	sorted bool,
	sysRootPresent bool,
	sysRootNonEmpty bool,
	localAppPresent bool,
	localAppNonEmpty bool,
) {
	provided = (envPtr != nil)
	unitCount = len(block)
	if len(block) < 2 {
		return provided, unitCount, false, false, false, false, false, false, false
	}
	if block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		return provided, unitCount, false, false, false, false, false, false, false
	}
	doubleNul = true

	// Empty block: 2 NULs
	if len(block) == 2 && block[0] == 0 && block[1] == 0 {
		return provided, unitCount, true, true, true, false, false, false, false
	}

	var rawEntries [][]uint16
	start := 0
	structValid = true
	for i := 0; i < len(block)-1; i++ {
		if block[i] == 0 {
			if i == start {
				break
			}
			rawEntries = append(rawEntries, block[start:i])
			start = i + 1
		}
	}
	if start != len(block)-1 {
		structValid = false
	}

	for _, e := range rawEntries {
		eq := -1
		for j, c := range e {
			if c == '=' {
				eq = j
				break
			}
		}
		if eq <= 0 {
			if len(e) >= 4 && e[0] == '=' && e[2] == ':' && e[3] == '=' {
				eq = 3
			} else {
				structValid = false
				continue
			}
		}
		k := e[:eq]
		v := e[eq+1:]
		if equalFoldU16(k, "SYSTEMROOT") {
			sysRootPresent = true
			if len(v) > 0 {
				sysRootNonEmpty = true
			}
		}
		if equalFoldU16(k, "LOCALAPPDATA") {
			localAppPresent = true
			if len(v) > 0 {
				localAppNonEmpty = true
			}
		}
	}

	sorted = structValid && len(rawEntries) > 0
	if structValid && len(rawEntries) > 1 {
		for i := 0; i < len(rawEntries)-1; i++ {
			k1 := extractKeyFromU16(rawEntries[i])
			k2 := extractKeyFromU16(rawEntries[i+1])
			c, err := compareEnvNames(k1, k2)
			if err != nil || c > 0 {
				sorted = false
				break
			}
		}
	}

	return provided, unitCount, doubleNul, structValid, sorted, sysRootPresent, sysRootNonEmpty, localAppPresent, localAppNonEmpty
}

func captureWindowsLaunchMetadata(
	creationFlags uint32,
	si *startupInfoEx,
	caps *securityCapabilities,
	handles []windows.Handle,
	envPtr *uint16,
	block []uint16,
	appPath, dirPath string,
	parentLookup fixedKeyLookup,
) *WindowsLaunchMetadata {
	meta := &WindowsLaunchMetadata{
		CreationFlags: creationFlags,
	}

	if si != nil {
		meta.StartupInfoCb = si.StartupInfo.Cb
		meta.StartupInfoExSize = uint32(unsafe.Sizeof(*si))
	}
	if caps != nil {
		meta.SecurityCapabilitiesSize = uint32(unsafe.Sizeof(*caps))
		meta.CapabilityCount = caps.CapabilityCount
		meta.CapabilityReserved = caps.Reserved
		meta.SIDPresent = (caps.AppContainerSid != nil)
		meta.SecurityCapabilitiesAttrSize = uint32(unsafe.Sizeof(*caps))
	}
	if len(handles) > 0 {
		meta.HandleListAttrSize = uint32(uintptr(len(handles)) * unsafe.Sizeof(handles[0]))
		meta.HandleListCount = len(handles)
	}
	if caps != nil && len(handles) > 0 {
		meta.PopulatedAttributeCount = 2
	} else if caps != nil || len(handles) > 0 {
		meta.PopulatedAttributeCount = 1
	}

	pProvided, uCount, dNul, sValid, sorted, cSRPres, cSRNonEmpty, cLAPres, cLANonEmpty := inspectEnvBlock(envPtr, block)
	meta.EnvPointerProvided = pProvided
	meta.EnvUTF16UnitCount = uCount
	meta.EnvDoubleNULValid = dNul
	meta.EnvStructureValid = sValid
	meta.EnvSorted = sorted
	meta.ChildSystemRootAnyPresent = cSRPres
	meta.ChildSystemRootAnyNonEmpty = cSRNonEmpty
	meta.ChildLocalAppDataAnyPresent = cLAPres
	meta.ChildLocalAppDataAnyNonEmpty = cLANonEmpty

	pSRPres, pSRNonEmpty := queryFixedKey(parentLookup, "SYSTEMROOT")
	pLAPres, pLANonEmpty := queryFixedKey(parentLookup, "LOCALAPPDATA")
	meta.ParentSystemRootPresent = pSRPres
	meta.ParentSystemRootNonEmpty = pSRNonEmpty
	meta.ParentLocalAppDataPresent = pLAPres
	meta.ParentLocalAppDataNonEmpty = pLANonEmpty

	meta.AppPathCategory = classifyWindowsPath(appPath)
	if appPath != "" {
		meta.AppPathUTF16Length = len(utf16.Encode([]rune(appPath)))
	}
	meta.CurrentDirCategory = classifyWindowsPath(dirPath)
	if dirPath != "" {
		meta.CurrentDirUTF16Length = len(utf16.Encode([]rune(dirPath)))
	}

	return meta
}

func formatCreateProcessError(err error, meta ...*WindowsLaunchMetadata) error {
	if err == nil {
		return nil
	}
	var errno windows.Errno
	hasErrno := errors.As(err, &errno)

	var metaSuffix string
	if len(meta) > 0 && meta[0] != nil {
		if raw, mErr := json.Marshal(meta[0]); mErr == nil {
			metaSuffix = fmt.Sprintf("; launch_metadata=%s", string(raw))
		}
	}

	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		if hasErrno {
			return fmt.Errorf("BLOCKED: AppContainer cannot start the executable (access denied). Refusing to rewrite ACLs outside the fixture temp or package folder. Missing capability: executable image readable by this AppContainer SID. Win32: (errno %d) %w%s", errno, err, metaSuffix)
		}
		return fmt.Errorf("BLOCKED: AppContainer cannot start the executable (access denied). Refusing to rewrite ACLs outside the fixture temp or package folder. Missing capability: executable image readable by this AppContainer SID. Win32: %w%s", err, metaSuffix)
	}

	if hasErrno {
		return fmt.Errorf("BLOCKED: CreateProcess AppContainer: (errno %d) %w%s", errno, err, metaSuffix)
	}
	return fmt.Errorf("BLOCKED: CreateProcess AppContainer: %w%s", err, metaSuffix)
}

func waitCtx(p *Prepared) <-chan struct{} {
	if p.ctx == nil {
		return nil
	}
	return p.ctx.Done()
}

func abandonLaunch(thread, process, job windows.Handle) error {
	if process == 0 {
		return fmt.Errorf("%w: no process to wait for", ErrDescendantsActive)
	}
	_ = windows.TerminateProcess(process, 1)
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
	}
	status, werr := windows.WaitForSingleObject(process, 2000)
	if qerr := settleWait(status, werr); qerr != nil {
		return qerr
	}
	_ = windows.CloseHandle(process)
	if thread != 0 {
		_ = windows.CloseHandle(thread)
	}
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
	return nil
}

func jobActiveProcesses(job windows.Handle) (int, error) {
	var info jobBasicAccounting
	err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	if err != nil {
		return -1, err
	}
	return int(info.ActiveProcesses), nil
}

type jobBasicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
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
		return duplicateInheritable(windows.Handle(f.Fd()))
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
		return duplicateInheritable(windows.Handle(f.Fd()))
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

func duplicateInheritable(src windows.Handle) (windows.Handle, func(), error) {
	var dup windows.Handle
	err := windows.DuplicateHandle(windows.CurrentProcess(), src, windows.CurrentProcess(), &dup, 0, true, windows.DUPLICATE_SAME_ACCESS)
	if err != nil {
		return 0, func() {}, err
	}
	return dup, func() { windows.CloseHandle(dup) }, nil
}

var systemRootLookup = defaultSystemRootLookup

func defaultSystemRootLookup() string {
	if v := os.Getenv("SystemRoot"); v != "" {
		return v
	}
	return os.Getenv("SYSTEMROOT")
}

// addCriticalEnv adds critical environment variables required on Windows
// (specifically SYSTEMROOT), following Go's os/exec addCriticalEnv convention.
// If SYSTEMROOT is already present (checked case-insensitively), it is retained.
// Other parent environment variables are not inherited.
func addCriticalEnv(env []string) []string {
	return addCriticalEnvWithLookup(env, systemRootLookup)
}

func addCriticalEnvWithLookup(env []string, lookup func() string) []string {
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(k, "SYSTEMROOT") {
			return env
		}
	}
	var systemRoot string
	if lookup != nil {
		systemRoot = lookup()
	}
	return append(slices.Clone(env), "SYSTEMROOT="+systemRoot)
}

// replenishChildLocalAppData replenishes missing LOCALAPPDATA environment variable
// in the child environment sourced strictly from the AppContainer AC root
// (rec.Root) in the ownership RunRecord.
// If LOCALAPPDATA is already present (checked case-insensitively), existing
// child values and case aliases are preserved.
// If LOCALAPPDATA is missing and the ownership root is missing or unusable,
// it fails closed without host fallback or guessing.
func replenishChildLocalAppData(env []string, rec *RunRecord) ([]string, error) {
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, "LOCALAPPDATA") {
			return env, nil
		}
	}
	if rec == nil || strings.TrimSpace(rec.Root) == "" {
		return nil, fmt.Errorf("%w: missing AppContainer ownership root for LOCALAPPDATA", ErrRefused)
	}
	return append(slices.Clone(env), "LOCALAPPDATA="+rec.Root), nil
}

func hasKeyFold(env []string, target string) bool {
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, target) {
			return true
		}
	}
	return false
}

func observeLocalAppDataProvenance(childEnv []string, ownedRoot string, wasExplicit bool) (isOwnedRoot bool, fromOwnedRoot bool) {
	if strings.TrimSpace(ownedRoot) == "" {
		return false, false
	}
	for _, kv := range childEnv {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, "LOCALAPPDATA") {
			isOwned := strings.EqualFold(filepath.Clean(v), filepath.Clean(ownedRoot))
			return isOwned, !wasExplicit && isOwned
		}
	}
	return false, false
}

func extractEnvName(entry string) string {
	for i := 0; i < len(entry); i++ {
		if entry[i] == '=' {
			if i == 0 {
				continue
			}
			return entry[:i]
		}
	}
	return entry
}

func compareEnvNames(u1, u2 []uint16) (int, error) {
	if len(u1) == 0 && len(u2) == 0 {
		return 0, nil
	}
	if len(u1) == 0 {
		return -1, nil
	}
	if len(u2) == 0 {
		return 1, nil
	}
	r, _, callErr := procCompareStringOrdinal.Call(
		uintptr(unsafe.Pointer(&u1[0])),
		uintptr(len(u1)),
		uintptr(unsafe.Pointer(&u2[0])),
		uintptr(len(u2)),
		1, // bIgnoreCase = TRUE (OS uppercase table, exactly 1)
	)
	switch r {
	case 1: // CSTR_LESS_THAN
		return -1, nil
	case 2: // CSTR_EQUAL
		return 0, nil
	case 3: // CSTR_GREATER_THAN
		return 1, nil
	case 0:
		return 0, fmt.Errorf("CompareStringOrdinal failed: %w", callErr)
	default:
		return 0, fmt.Errorf("CompareStringOrdinal returned unexpected code %d", r)
	}
}

func envBlock(env []string) (*uint16, []uint16, error) {
	if len(env) == 0 {
		return nil, nil, nil
	}

	type envEntry struct {
		name []uint16
		u16  []uint16
	}

	entries := make([]envEntry, len(env))
	for i, kv := range env {
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, nil, err
		}
		name := extractEnvName(kv)
		entries[i] = envEntry{
			name: utf16.Encode([]rune(name)),
			u16:  u,
		}
	}

	var sortErr error
	slices.SortStableFunc(entries, func(a, b envEntry) int {
		if sortErr != nil {
			return 0
		}
		c, err := compareEnvNames(a.name, b.name)
		if err != nil {
			sortErr = err
			return 0
		}
		return c
	})
	if sortErr != nil {
		return nil, nil, sortErr
	}

	var block []uint16
	for _, e := range entries {
		block = append(block, e.u16...)
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

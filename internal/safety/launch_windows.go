//go:build windows

package safety

import (
	"fmt"
	"io"
	"os"
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
		extendedStartupInfoPresent|createUnicodeEnvironment|windows.CREATE_SUSPENDED,
		env, dir, &si.StartupInfo, &pi,
	)
	if err != nil {
		if err == windows.ERROR_ACCESS_DENIED {
			return fmt.Errorf("BLOCKED: AppContainer cannot start the executable (access denied). Refusing to rewrite ACLs outside the fixture temp or package folder. Missing capability: executable image readable by this AppContainer SID. Win32: %v", err)
		}
		return fmt.Errorf("BLOCKED: CreateProcess AppContainer: %v", err)
	}
	proc, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		abandonLaunch(pi.Thread, pi.Process, 0)
		return err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		abandonLaunch(pi.Thread, pi.Process, 0)
		return fmt.Errorf("BLOCKED: CreateJobObject: %v", err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		abandonLaunch(pi.Thread, pi.Process, job)
		return fmt.Errorf("BLOCKED: job lifecycle limit: %v", err)
	}
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		abandonLaunch(pi.Thread, pi.Process, job)
		return fmt.Errorf("BLOCKED: AssignProcessToJobObject: %v", err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		abandonLaunch(pi.Thread, pi.Process, job)
		return fmt.Errorf("BLOCKED: ResumeThread: %v", err)
	}
	_ = windows.CloseHandle(pi.Thread)
	p.pid = int(pi.ProcessId)
	stop := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-stop:
		case <-waitCtx(p):
			_ = windows.TerminateJobObject(job, 1)
		}
	}()
	p.waitFn = func() error {
		close(stop)
		<-joined
		state, waitErr := proc.Wait()
		active, qerr := jobActiveProcesses(job)
		if qerr != nil || active != 0 {
			_ = windows.TerminateJobObject(job, 1)
			windows.WaitForSingleObject(pi.Process, 2000)
			active, qerr = jobActiveProcesses(job)
		}
		if qerr != nil || active != 0 {
			return fmt.Errorf("%w: active=%d query=%v", ErrDescendantsActive, active, qerr)
		}
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(job)
		for _, c := range closers {
			c()
		}
		if err := cleanupAllowed(Lifecycle{HelperExited: true, CancelJoined: true, HandlesReleased: true, ActiveProcesses: active}); err != nil {
			return err
		}
		code := 0
		if state != nil {
			code = state.ExitCode()
		}
		return exitFromWait(code, waitErr)
	}
	p.killFn = func() error {
		return windows.TerminateJobObject(job, 1)
	}
	started = true
	return nil
}

func waitCtx(p *Prepared) <-chan struct{} {
	if p.ctx == nil {
		return nil
	}
	return p.ctx.Done()
}

func abandonLaunch(thread, process, job windows.Handle) {
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
	} else if process != 0 {
		_ = windows.TerminateProcess(process, 1)
	}
	if process != 0 {
		_, _ = windows.WaitForSingleObject(process, 2000)
		_ = windows.CloseHandle(process)
	}
	if thread != 0 {
		_ = windows.CloseHandle(thread)
	}
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
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

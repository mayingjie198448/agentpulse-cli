//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// processSynchronize is the Win32 SYNCHRONIZE access right, mirroring
// internal/wrapper/process_windows.go:16. x/sys does not export it next to
// the PROCESS_* rights; the product adds it in the same way.
const processSynchronize = 0x00100000

// stillActive mirrors internal/wrapper/process_windows.go:19.
const stillActive = 259

// waitTimeoutStatus mirrors internal/wrapper/process_windows.go:23.
const waitTimeoutStatus = 0x102

// listenerForcedExit is the synthetic exit code passed to TerminateProcess,
// mirroring the product pattern at process_windows.go:381. It is recorded as a
// force-terminate class and is NOT counted as a clean listener exit: the raw
// code is kept end to end, and only a raw 0 reads as clean.
const listenerForcedExit = 0xC000013A

// graceWaitMs bounds how long the listener we started may shut down on its own
// after the control event, before the driver is allowed to force termination.
const graceWaitMs = 5000

// Identity-bound listener state, mirroring the production pattern in
// internal/wrapper/process_windows.go:117 observeWindowsExitFor: the handle we
// open right after spawning stays open until we observe the exit or terminate
// the process, so GetProcessId, GetProcessTimes and GetExitCodeProcess keep
// referring to OUR child even if the pid number is recycled meanwhile.
var listenerBoundHandle = windows.Handle(0)
var listenerAttached = false
var listenerPid = 0
var listenerCreationNs = int64(0)
var listenerExitObserved = false
var listenerExitCode = 0
var listenerTerminationNote = "none"

// graceNotes records every step of the graceful-stop sequence (send, wait,
// force, re-wait), including the failures, so the evidence names what actually
// happened instead of implying a clean stop.
var graceNotes []string

func setupListenerSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// stopListener recovers the graceful-stop sequence:
// 1. Send CTRL_BREAK to listener process group
// 2. Bounded wait on bound handle
// 3. Force-terminate bound handle if still running (recording termination failures)
// 4. Post-termination re-wait and confirm result.
func stopListener(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if !listenerAttached {
		graceNotes = append(graceNotes, "no bound handle: sending CTRL_BREAK to direct child pid")
		err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
		if err != nil {
			graceNotes = append(graceNotes, fmt.Sprintf("GenerateConsoleCtrlEvent failed: %v", err))
			listenerTerminationNote = "no bound handle: GenerateConsoleCtrlEvent failed"
			return err
		}
		listenerTerminationNote = "no bound handle: GenerateConsoleCtrlEvent sent"
		return nil
	}

	// 1. Send CTRL_BREAK
	graceNotes = append(graceNotes, fmt.Sprintf("send CTRL_BREAK to process group %d", listenerPid))
	ctrlErr := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(listenerPid))
	if ctrlErr != nil {
		graceNotes = append(graceNotes, fmt.Sprintf("send CTRL_BREAK failed: %v", ctrlErr))
	} else {
		graceNotes = append(graceNotes, "send CTRL_BREAK succeeded")
	}

	// 2. Bounded wait on bound handle
	graceNotes = append(graceNotes, fmt.Sprintf("bounded wait (%d ms) on bound handle", graceWaitMs))
	waitStatus, waitErr := windows.WaitForSingleObject(listenerBoundHandle, uint32(graceWaitMs))
	if waitErr != nil {
		graceNotes = append(graceNotes, fmt.Sprintf("bounded wait error: %v", waitErr))
	} else if waitStatus == windows.WAIT_OBJECT_0 {
		graceNotes = append(graceNotes, "listener exited gracefully within bounded wait")
		listenerTerminationNote = "graceful-stop (CTRL_BREAK succeeded within bounded wait)"
		return nil
	} else if waitStatus == waitTimeoutStatus {
		graceNotes = append(graceNotes, "bounded wait timed out; listener still running")
	} else {
		graceNotes = append(graceNotes, fmt.Sprintf("bounded wait returned unexpected status: 0x%x", waitStatus))
	}

	// 3. Force-terminate bound handle if still running
	confirmBoundIdentity("before-force-terminate")
	graceNotes = append(graceNotes, fmt.Sprintf("force-terminating bound handle (pid %d) with exit code 0x%X", listenerPid, uint32(listenerForcedExit)))
	termErr := windows.TerminateProcess(listenerBoundHandle, uint32(listenerForcedExit))
	if termErr != nil {
		graceNotes = append(graceNotes, fmt.Sprintf("TerminateProcess failed: %v", termErr))
		listenerTerminationNote = fmt.Sprintf("force-terminate failed: %v", termErr)
		return fmt.Errorf("c4-e2e-driver: TerminateProcess failed: %w", termErr)
	}
	graceNotes = append(graceNotes, "TerminateProcess issued to bound handle")

	// 4. Re-wait and record result
	graceNotes = append(graceNotes, "post-termination wait (5000 ms) on bound handle")
	postStatus, postErr := windows.WaitForSingleObject(listenerBoundHandle, 5000)
	if postErr != nil {
		graceNotes = append(graceNotes, fmt.Sprintf("post-termination wait error: %v", postErr))
	} else if postStatus == windows.WAIT_OBJECT_0 {
		graceNotes = append(graceNotes, "bound handle confirmed terminated in post-termination wait")
	} else {
		graceNotes = append(graceNotes, fmt.Sprintf("post-termination wait returned status: 0x%x", postStatus))
	}
	confirmBoundIdentity("after-post-wait")
	listenerTerminationNote = fmt.Sprintf("forced-termination (pid %d terminated with code 0x%X)", listenerPid, uint32(listenerForcedExit))
	return nil
}

// isProcessAlive is a convenience check ONLY: a fresh OpenProcess by pid cannot
// prove identity (a recycled pid is a false pass, and QUERY_LIMITED_INFORMATION
// can AccessDenied a live process for a false fail). The reclaim proof must
// come from the handle bound at attach time, see listenerReclaimProof.
func isProcessAlive(pid int) bool {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if e != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}

// Item 2 (re-review 2026-11-21): only a raw exit code of 0 reads as clean.
func isCleanListenerExit(raw int) bool {
	return raw == 0
}

// attachListenerHandle mirrors internal/wrapper/process_windows.go observeWindowsExitFor
// (:117) plus the OpenProcess attach (:205): handle for the just-started pid, GetProcessId
// identity confirmation, creation time read from the SAME handle, and the handle kept
// open until the exit is observed or termination happens (CloseHandle only there, never
// before). An already-departed pid is refused, as the product does at :181 alreadyGone.
func attachListenerHandle(cmd *exec.Cmd) (stamp string, err error) {
	if cmd == nil || cmd.Process == nil {
		return "not-attached", errors.New("listener process handle unavailable")
	}
	listenerPid = cmd.Process.Pid
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|processSynchronize, false, uint32(listenerPid))
	if e != nil {
		return "not-attached", errors.New("attach: OpenProcess failed for the just-started pid")
	}
	confirmed, e2 := windows.GetProcessId(h)
	if e2 != nil || int(confirmed) != listenerPid {
		_ = windows.CloseHandle(h)
		return "not-attached", errors.New("attach: bound handle does not name the listener pid")
	}
	var created windows.Filetime
	var exited windows.Filetime
	var kernel windows.Filetime
	var user windows.Filetime
	e3 := windows.GetProcessTimes(h, &created, &exited, &kernel, &user)
	if e3 != nil || exited.Nanoseconds() != 0 {
		_ = windows.CloseHandle(h)
		return "not-attached", errors.New("attach: creation time unavailable or pid already exited")
	}
	_ = kernel
	_ = user
	_ = exited
	// Keep the handle open until the exit is observed or termination happens,
	// mirroring process_windows.go:135 where CloseHandle lives.
	listenerBoundHandle = h
	listenerAttached = true
	listenerCreationNs = created.Nanoseconds()
	st := time.Unix(0, listenerCreationNs).Format(time.RFC3339Nano)
	stamp = st
	return "creation-ns=" + fmt.Sprint(listenerCreationNs) + " stamp=" + stamp, nil
}

// listenerReclaimProof is identity-bound evidence: the exit code read from the
// handle attached at start time, whose pid AND creation time are still confirmed
// to name our listener. It never re-opens a handle by pid number as a proof.
func listenerReclaimProof(cmd *exec.Cmd) (proof string, raw int, ok bool) {
	_ = cmd
	if !listenerAttached {
		return "no-bound-handle: the listener handle was never attached", 0, false
	}
	confirmed, e := windows.GetProcessId(listenerBoundHandle)
	if e != nil || int(confirmed) != listenerPid {
		return "bound-handle identity broken: GetProcessId(handle) no longer names the listener pid", 0, false
	}
	var created2 windows.Filetime
	var exited2 windows.Filetime
	var kernel2 windows.Filetime
	var user2 windows.Filetime
	e2 := windows.GetProcessTimes(listenerBoundHandle, &created2, &exited2, &kernel2, &user2)
	if e2 != nil || created2.Nanoseconds() != listenerCreationNs {
		return "bound-handle identity broken: creation time no longer matches the attach-time value", 0, false
	}
	_ = exited2
	_ = kernel2
	_ = user2
	// Distinguish exited from still-running before trusting any exit code
	// (product waitExit, process_windows.go:237).
	status, e3 := windows.WaitForSingleObject(listenerBoundHandle, 0)
	if e3 != nil || status == windows.WAIT_FAILED {
		return "wait-state unknown: WaitForSingleObject failed on the bound handle", 0, false
	}
	if status == waitTimeoutStatus {
		return "listener still running when probed: no exit observed yet", stillActive, false
	}
	if status != windows.WAIT_OBJECT_0 {
		return "wait-state unknown: WaitForSingleObject returned an unexpected status", 0, false
	}
	var exitCode uint32
	e4 := windows.GetExitCodeProcess(listenerBoundHandle, &exitCode)
	if e4 != nil {
		return "exit code unavailable: GetExitCodeProcess failed on the bound handle", 0, false
	}
	if exitCode == stillActive {
		return "weak: exit code read without an observed exit-status event", int(exitCode), false
	}
	listenerExitObserved = true
	listenerExitCode = int(exitCode)
	proof = "bound-handle exit status: pid=" + fmt.Sprint(listenerPid) + " creation-ns=" + fmt.Sprint(listenerCreationNs) + " exit-code=" + fmt.Sprint(listenerExitCode)
	return proof, listenerExitCode, true
}

// terminateListenerIfRunning terminates the bound handle if still running.
func terminateListenerIfRunning(cmd *exec.Cmd) error {
	if !listenerAttached {
		if cmd == nil || cmd.Process == nil {
			listenerTerminationNote = "no bound handle: direct child already exited, no signal sent"
			return nil
		}
		listenerTerminationNote = "no bound handle: live direct child, Kill"
		return cmd.Process.Kill()
	}
	status, e := windows.WaitForSingleObject(listenerBoundHandle, 0)
	if e != nil || status == windows.WAIT_FAILED {
		listenerTerminationNote = "wait state unknown: no termination attempted"
		return errors.New("c4-e2e-driver: wait state unknown on bound handle")
	}
	if status == windows.WAIT_OBJECT_0 {
		listenerTerminationNote = "already exited: no termination signal needed"
		return nil
	}
	confirmBoundIdentity("before-direct-terminate")
	ce := uint32(listenerForcedExit)
	e5 := windows.TerminateProcess(listenerBoundHandle, ce)
	if e5 != nil {
		listenerTerminationNote = fmt.Sprintf("bound-handle TerminateProcess failed: %v", e5)
		return fmt.Errorf("c4-e2e-driver: TerminateProcess failed: %w", e5)
	}
	// Post-termination re-wait
	_, _ = windows.WaitForSingleObject(listenerBoundHandle, 5000)
	confirmBoundIdentity("after-direct-terminate")
	listenerTerminationNote = fmt.Sprintf("forced-termination (pid %d terminated with code 0x%X)", listenerPid, ce)
	return nil
}

// confirmBoundIdentity re-reads GetProcessId and the creation time on the bound handle.
func confirmBoundIdentity(when string) {
	confirmed, e := windows.GetProcessId(listenerBoundHandle)
	if e != nil || int(confirmed) != listenerPid {
		graceNotes = append(graceNotes, "bound-handle identity broken at "+when+": GetProcessId no longer names the listener pid")
		return
	}
	var created2 windows.Filetime
	var exited2 windows.Filetime
	var kernel2 windows.Filetime
	var user2 windows.Filetime
	e2 := windows.GetProcessTimes(listenerBoundHandle, &created2, &exited2, &kernel2, &user2)
	if e2 != nil || created2.Nanoseconds() != listenerCreationNs {
		graceNotes = append(graceNotes, "bound-handle identity broken at "+when+": creation time no longer matches the attach-time value")
		return
	}
	_ = exited2
	_ = kernel2
	_ = user2
	graceNotes = append(graceNotes, "bound-handle identity confirmed at "+when)
}

// listenerGraceEvents reports the graceful-stop sequence for the evidence.
func listenerGraceEvents() []string {
	out := make([]string, len(graceNotes))
	copy(out, graceNotes)
	return out
}

// listenerTerminationVia reports which termination path was taken.
func listenerTerminationVia() string {
	return listenerTerminationNote
}

// releaseListenerHandle closes the bound handle exactly once at end of run.
func releaseListenerHandle() error {
	if listenerBoundHandle == 0 {
		return nil
	}
	h := listenerBoundHandle
	listenerBoundHandle = 0
	return windows.CloseHandle(h)
}

// isLinkOrReparse checks whether fi is a symlink, irregular, or has FILE_ATTRIBUTE_REPARSE_POINT.
func isLinkOrReparse(fi os.FileInfo, path string) bool {
	if fi == nil {
		return false
	}
	if fi.Mode()&os.ModeSymlink != 0 || fi.Mode()&os.ModeIrregular != 0 {
		return true
	}
	const fileAttributeReparsePoint = 0x00000400
	if sys, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		if sys.FileAttributes&fileAttributeReparsePoint != 0 {
			return true
		}
	}
	return false
}

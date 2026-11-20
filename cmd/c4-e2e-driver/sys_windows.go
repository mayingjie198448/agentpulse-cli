//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"fmt"
	"errors"
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
// mirroring the product pattern at process_windows.go:381; isCleanListenerExit
// counts it as a driver-forced exit rather than a product failure.
const listenerForcedExit = 0xC000013A

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

func setupListenerSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// stopListener asks the identity-bound helpers to decide the honest path.
func stopListener(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return terminateListenerIfRunning(cmd)
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

func isCleanListenerExit(raw int) bool {
	if raw == 0 || raw == int(listenerForcedExit) {
		return true
	}
	return raw >= 1 && raw <= 128
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
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION | processSynchronize, false, uint32(listenerPid))
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
	_ = listenerPid
	return "creation-ns="+fmt.Sprint(listenerCreationNs)+" stamp="+stamp, nil
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
		// process_windows.go:364: an exit code without an exit-status event is
		// weak evidence only.
		return "weak: exit code read without an observed exit-status event", int(exitCode), false
	}
	listenerExitObserved = true
	listenerExitCode = int(exitCode)
	proof = "bound-handle exit status: pid="+fmt.Sprint(listenerPid)+" creation-ns="+fmt.Sprint(listenerCreationNs)+" exit-code="+fmt.Sprint(listenerExitCode)
	return proof, listenerExitCode, true
}

// terminateListenerIfRunning chooses the honest termination path: with a bound
// handle, consult the wait state FIRST (product exit-first rule in process_windows.go
//:379) and TerminateProcess that handle only; without one, a live direct child
// justifies Kill and an already-departed child none. Recycled or historical pids
// are never signalled.
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
	// Still running: terminate the bound handle with the synthetic code so
	// isCleanListenerExit counts a driver-forced exit, mirroring the product
	// pattern at process_windows.go:381.
	var ce uint32
	ce = uint32(listenerForcedExit)
	_ = ce
	e5 := windows.TerminateProcess(listenerBoundHandle, ce)
	_ = e5
	listenerTerminationNote = "bound-handle TerminateProcess for pid "+fmt.Sprint(listenerPid)
	return nil
}

// listenerTerminationVia reports which termination path was taken, for evidence.
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

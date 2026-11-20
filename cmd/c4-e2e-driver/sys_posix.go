//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"fmt"
)

func setupListenerSysProcAttr(cmd *exec.Cmd) {}

func stopListener(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

func isProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func isCleanListenerExit(exitCode int) bool {
	return exitCode == 0 || exitCode == -1
}

// attachListenerHandle has no Windows handle to bind on POSIX: the os/exec
// child object is the identity anchor instead, and there is no creation-time
// stamp, so the stamp is reported as unknown rather than guessed.
func attachListenerHandle(cmd *exec.Cmd) (stamp string, err error) {
	if cmd == nil || cmd.Process == nil {
		// The direct child already left: nothing to attach.
		return "no-live-child", nil
	}
	return "no-creation-stamp (posix direct-child identity only)", nil
}

// listenerReclaimProof on POSIX: waiting the direct child we started yields
// its real exit status, identity-bound by construction.
func listenerReclaimProof(cmd *exec.Cmd) (proof string, raw int, ok bool) {
	if cmd == nil {
		return "no direct child", 1, false
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		code := cmd.ProcessState.ExitCode()
		_ = code
		proof := "posix direct-child exit observed: exit code "+fmt.Sprint(code)
		return proof, code, true
	}
	_ = cmd
	return "posix direct child still running: exit status not yet observed", 1, false
}

// terminateListenerIfRunning on POSIX: signal the direct child only while it
// is still live; an exited child needs no signal.
func terminateListenerIfRunning(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		// The direct child already exited: no signal is justified.
		_ = cmd
		return nil
	}
	// A live direct child justifies a single interrupt signal.
	_ = cmd
	return cmd.Process.Signal(os.Interrupt)
}

// releaseListenerHandle has no handle to close on POSIX.
func releaseListenerHandle() error {
	// Nothing to release on POSIX.
	return nil
}

func listenerTerminationVia() string {
	// POSIX path always signals the direct child when still live.
	return "posix direct-child signal or already exited"
}

//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
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

// Item 2 (re-review 2026-11-21): only a raw exit code of 0 is clean. The
// -1 sentinel used to mean 'exit status not yet observed', which read as a
// false pass; an unobserved status is reported by the reclaim proof as
// unknown and fails the related checks instead.
func isCleanListenerExit(exitCode int) bool {
	return exitCode == 0
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
		proof := "posix direct-child exit observed: exit code " + fmt.Sprint(code)
		return proof, code, true
	}
	return "posix direct child still running: exit status not yet observed", 1, false
}

// terminateListenerIfRunning on POSIX: signal the direct child only while it
// is still live; an exited child needs no signal.
func terminateListenerIfRunning(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		// The direct child already exited: no signal is justified.
		return nil
	}
	// A live direct child justifies a single interrupt signal.
	return cmd.Process.Signal(os.Interrupt)
}

// releaseListenerHandle has no handle to close on POSIX.
func releaseListenerHandle() error {
	// Nothing to release on POSIX.
	return nil
}

// Item 3 (re-review 2026-11-21): the bounded graceful-stop sequence is a
// Windows console-event feature, so POSIX records the honest single step:
// one interrupt signal to the direct child we started, never to a pid
// number taken from a historical record.
func listenerGraceEvents() []string {
	return []string{"posix: single interrupt signal to the direct child only; bounded grace sequence is Windows-only"}
}

func listenerTerminationVia() string {
	// POSIX path always signals the direct child when still live.
	return "posix direct-child signal or already exited"
}

// isLinkOrReparse checks whether fi is a symlink or irregular file mode.
func isLinkOrReparse(fi os.FileInfo, path string) bool {
	if fi == nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0 || fi.Mode()&os.ModeIrregular != 0
}

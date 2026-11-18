//go:build !windows

package main

import (
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

func isCleanListenerExit(exitCode int) bool {
	return exitCode == 0 || exitCode == -1
}

// Synthetic agy for allowlist isolation tests.
// It holds one private log file open and sleeps. It does not read the host
// process table, config, or any other process.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if os.Getenv("AGENTPULSE_SYNTHETIC_DETACH") == "1" {
		detach()
		return
	}
	hold()
}

func detach() {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "AGENTPULSE_SYNTHETIC_DETACH=") {
			continue
		}
		env = append(env, entry)
	}
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Env = env
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%d\n", cmd.Process.Pid)
	_ = cmd.Process.Release()
}

func hold() {
	logPath := os.Getenv("AGENTPULSE_SYNTHETIC_LOG")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "Starting language server process with pid %d\n", os.Getpid()); err != nil {
		os.Exit(3)
	}
	if len(os.Args) > 1 {
		if _, err := fmt.Fprintf(f, "argv %s\n", strings.Join(os.Args, " ")); err != nil {
			os.Exit(3)
		}
	}
	_ = f.Sync()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sig:
	case <-time.After(3 * time.Minute):
	}
}

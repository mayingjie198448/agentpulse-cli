//go:build !windows

package wrapper

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultProcessLister lists processes with POSIX ps.
// Windows uses process_windows.go instead of this file.
func DefaultProcessLister(ctx context.Context) ([]ProcessInfo, error) {
	return listWithPS(ctx, nil)
}

func listDefaultAllowlisted(ctx context.Context, pids []int) ([]ProcessInfo, error) {
	if runtime.GOOS == "linux" {
		return listLinuxPIDs(pids)
	}
	return listWithPS(ctx, pids)
}

func defaultBoundExitChecker() func(pid int, started time.Time) (int, bool) {
	return nil
}

// ObservedStartTime is not used to contradict POSIX ps results.
// The ps fallback can invent the current time, which must not look like reuse.
func ObservedStartTime(int) (time.Time, bool) {
	return time.Time{}, false
}

// ResolveProcessCwd probes the current working directory of a process.
func ResolveProcessCwd(pid int) string {
	if pid <= 0 {
		return ""
	}

	if runtime.GOOS == "linux" {
		if target, err := readProcLink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
			return target
		}
	}

	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		cmd := commandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
		out, err := cmd.Output()
		if err == nil {
			lines := strings.Split(string(out), "\n")
			for _, l := range lines {
				if strings.HasPrefix(l, "n") {
					path := strings.TrimPrefix(l, "n")
					if path != "" && path != "/" {
						return path
					}
				}
			}
		}
	}

	return ""
}

// ResolveProcessStartTime probes the process start time.
func ResolveProcessStartTime(pid int) time.Time {
	if pid <= 0 {
		return time.Now().UTC()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmd := commandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	out, err := cmd.Output()
	if err == nil {
		str := strings.TrimSpace(string(out))
		layouts := []string{
			"Mon Jan _2 15:04:05 2006",
			"Mon Jan 2 15:04:05 2006",
			time.ANSIC,
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, str); err == nil {
				return t.UTC()
			}
		}
	}

	return time.Now().UTC()
}

// IsProcessAlive reports whether a process with the given PID is currently running.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}

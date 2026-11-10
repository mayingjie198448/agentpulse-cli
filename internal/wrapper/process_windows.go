//go:build windows

package wrapper

import (
	"context"
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processSynchronize is the Win32 SYNCHRONIZE access right.
// x/sys does not export it next to the PROCESS_* rights.
const processSynchronize = 0x00100000

// stillActive is the GetExitCodeProcess value for a process that has not exited.
const stillActive = 259

// waitTimeoutStatus is the WaitForSingleObject result for a running process.
// windows.WAIT_TIMEOUT is an errno, not this wait status.
const waitTimeoutStatus = 0x102

// DefaultProcessLister lists processes with Toolhelp32.
// Each returned row is limited to what QUERY_LIMITED_INFORMATION can read.
// Cwd is left empty. Processes whose start time cannot be read are omitted
// so a later PID reuse cannot inherit their identity.
func DefaultProcessLister(ctx context.Context) ([]ProcessInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return listWindowsSnapshot(nil)
}

func listDefaultAllowlisted(ctx context.Context, pids []int) ([]ProcessInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	allow := make(map[uint32]bool, len(pids))
	for _, pid := range pids {
		if pid > 0 {
			allow[uint32(pid)] = true
		}
	}
	return listWindowsSnapshot(allow)
}

func defaultExitChecker() func(int) (int, bool) {
	return observeWindowsExit
}

// ResolveProcessCwd has no PROCESS_QUERY_LIMITED_INFORMATION result on Windows.
// Reading the PEB would require PROCESS_VM_READ, so the cwd stays unread.
func ResolveProcessCwd(pid int) string {
	return AcceptCWD("", false)
}

// ResolveProcessStartTime returns the creation time from GetProcessTimes.
// Failure returns the zero time. It does not substitute the current time.
func ResolveProcessStartTime(pid int) time.Time {
	started, ok := ObservedStartTime(pid)
	if !ok {
		return time.Time{}
	}
	return started
}

// ObservedStartTime reads the process creation time.
// The boolean is false when the process cannot be queried.
func ObservedStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	h, err := openQuery(uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)
	return creationTime(h)
}

// IsProcessAlive reports a queried process that has not been signaled.
// A process that cannot be opened is not reported as alive.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := openQuery(uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	alive, _, ok := waitExit(h)
	return ok && alive
}

func observeWindowsExit(pid int) (int, bool) {
	if pid <= 0 {
		return 0, false
	}
	h, err := openQuery(uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	alive, code, ok := waitExit(h)
	obs := ObserveExit(false, alive, ExitObservation{Code: code, Known: ok && !alive})
	return obs.Code, obs.Known
}

func listWindowsSnapshot(allow map[uint32]bool) ([]ProcessInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	err = windows.Process32First(snap, &entry)
	if err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil, nil
		}
		return nil, err
	}

	var out []ProcessInfo
	for {
		pid := entry.ProcessID
		if pid > 0 && (allow == nil || allow[pid]) {
			if info, ok := inspectWindowsProcess(pid, entry.ParentProcessID, windows.UTF16ToString(entry.ExeFile[:])); ok {
				out = append(out, info)
			}
		}
		err = windows.Process32Next(snap, &entry)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, err
		}
	}
	return out, nil
}

func inspectWindowsProcess(pid, ppid uint32, exeName string) (ProcessInfo, bool) {
	h, err := openQuery(pid)
	if err != nil {
		return ProcessInfo{}, false
	}
	defer windows.CloseHandle(h)

	started, ok := creationTime(h)
	if !ok || started.IsZero() {
		return ProcessInfo{}, false
	}
	image := queryImagePath(h)
	if image == "" {
		image = exeName
	}
	if image == "" {
		return ProcessInfo{}, false
	}
	return ProcessInfo{
		PID:       int(pid),
		PPID:      int(ppid),
		StartTime: started,
		Args:      []string{image},
		Cwd:       AcceptCWD("", false),
	}, true
}

func openQuery(pid uint32) (windows.Handle, error) {
	return windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|processSynchronize, false, pid)
}

func creationTime(h windows.Handle) (time.Time, bool) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	started := time.Unix(0, created.Nanoseconds()).UTC()
	if started.IsZero() {
		return time.Time{}, false
	}
	return started, true
}

func queryImagePath(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil || n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// waitExit uses the process handle. STILL_ACTIVE is not treated as an exit
// code unless the handle has been signaled.
func waitExit(h windows.Handle) (alive bool, code int, ok bool) {
	status, err := windows.WaitForSingleObject(h, 0)
	if err != nil || status == windows.WAIT_FAILED {
		return false, 0, false
	}
	if status == waitTimeoutStatus {
		return true, 0, true
	}
	if status != windows.WAIT_OBJECT_0 {
		return false, 0, false
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false, 0, false
	}
	if exitCode == stillActive {
		return true, 0, true
	}
	return false, int(exitCode), true
}

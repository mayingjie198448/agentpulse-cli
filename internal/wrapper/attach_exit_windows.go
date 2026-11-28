//go:build windows

package wrapper

import (
	"sync"

	"golang.org/x/sys/windows"
)

// stillActive is the Win32 STILL_ACTIVE pseudo exit code.
const stillActive uint32 = 259

type windowsExitObserver struct {
	mu      sync.Mutex
	handles map[int]windows.Handle
	codes   map[int]int
	closed  bool
}

func newExitObserver() exitObserver {
	return &windowsExitObserver{
		handles: make(map[int]windows.Handle),
		codes:   make(map[int]int),
	}
}

func (o *windowsExitObserver) watch(pid int) {
	if pid <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	if _, ok := o.handles[pid]; ok {
		return
	}
	// Holding a handle keeps the exit code queryable after the process dies.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return
	}
	o.handles[pid] = h
}

func (o *windowsExitObserver) exitCode(pid int) (int, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if code, ok := o.codes[pid]; ok {
		return code, true
	}
	h, ok := o.handles[pid]
	if !ok {
		return 0, false
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code == stillActive {
		return 0, false
	}
	o.codes[pid] = int(code)
	return int(code), true
}

func (o *windowsExitObserver) unwatch(pid int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.codes, pid)
	if h, ok := o.handles[pid]; ok {
		delete(o.handles, pid)
		_ = windows.CloseHandle(h)
	}
}

func (o *windowsExitObserver) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	for pid, h := range o.handles {
		_ = windows.CloseHandle(h)
		delete(o.handles, pid)
	}
}

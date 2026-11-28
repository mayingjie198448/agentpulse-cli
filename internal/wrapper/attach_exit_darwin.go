//go:build darwin

package wrapper

import (
	"sync"

	"golang.org/x/sys/unix"
)

type darwinExitObserver struct {
	mu      sync.Mutex
	kq      int
	codes   map[int]int
	watched map[int]bool
	closed  bool
	done    chan struct{}
}

func newExitObserver() exitObserver {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil
	}
	o := &darwinExitObserver{
		kq:      kq,
		codes:   make(map[int]int),
		watched: make(map[int]bool),
		done:    make(chan struct{}),
	}
	go o.loop()
	return o
}

func (o *darwinExitObserver) loop() {
	defer close(o.done)
	events := make([]unix.Kevent_t, 8)
	// A timeout lets the loop notice close(); closing the kq alone does not
	// wake a blocked kevent on darwin.
	timeout := unix.Timespec{Nsec: 200 * 1000 * 1000}
	for {
		o.mu.Lock()
		closed := o.closed
		o.mu.Unlock()
		if closed {
			return
		}
		n, err := unix.Kevent(o.kq, nil, events, &timeout)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		for i := 0; i < n; i++ {
			pid := int(events[i].Ident)
			code := decodeExitStatus(events[i].Data)
			o.mu.Lock()
			// An event queued before unwatch must not resurrect a stale
			// code for a later reused PID.
			if o.watched[pid] {
				o.codes[pid] = code
			}
			o.mu.Unlock()
		}
	}
}

func (o *darwinExitObserver) watch(pid int) {
	if pid <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.watched[pid] {
		return
	}
	change := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE,
		// NOTE_EXIT alone delivers data=0; NOTE_EXITSTATUS carries the
		// wait4-style exit status.
		Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS,
	}
	if _, err := unix.Kevent(o.kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		// Already gone or not observable; stay conservative.
		return
	}
	delete(o.codes, pid)
	o.watched[pid] = true
}

func (o *darwinExitObserver) exitCode(pid int) (int, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	code, ok := o.codes[pid]
	return code, ok
}

func (o *darwinExitObserver) unwatch(pid int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.codes, pid)
	if !o.watched[pid] {
		return
	}
	delete(o.watched, pid)
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_DELETE}
	_, _ = unix.Kevent(o.kq, []unix.Kevent_t{change}, nil, nil)
}

func (o *darwinExitObserver) close() {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	o.mu.Unlock()
	<-o.done
	_ = unix.Close(o.kq)
}

// decodeExitStatus decodes the wait4-style status delivered by NOTE_EXIT.
func decodeExitStatus(status int64) int {
	s := int(status)
	if s&0x7f == 0 {
		return (s >> 8) & 0xff
	}
	return 128 + (s & 0x7f)
}

package wrapper

import "time"

// ProcessIdentity is a PID plus the start time used to detect reuse.
// A zero Start means the start time was not observed.
type ProcessIdentity struct {
	PID   int
	Start time.Time
}

// ExitObservation is an exit code the platform actually read.
// Known is false when the code must not be reported.
type ExitObservation struct {
	Code  int
	Known bool
}

// SameProcess reports whether a and b are the same process.
// known is false when either start time is missing; same is then false.
// Callers must not treat an unknown result as a match or as a new process's data.
func SameProcess(a, b ProcessIdentity) (same, known bool) {
	if a.PID <= 0 || b.PID <= 0 || a.PID != b.PID {
		return false, true
	}
	if a.Start.IsZero() || b.Start.IsZero() {
		return false, false
	}
	return a.Start.Equal(b.Start), true
}

// ObserveExit drops a probed code when the PID was reused or the process is
// still alive. A reused PID's code belongs to the new process and is discarded.
func ObserveExit(reused, alive bool, probed ExitObservation) ExitObservation {
	if reused || alive || !probed.Known {
		return ExitObservation{}
	}
	return probed
}

// AcceptCWD returns path only when the platform read it.
// An unread or empty result stays empty; it is not replaced with a guess.
func AcceptCWD(path string, read bool) string {
	if !read || path == "" {
		return ""
	}
	return path
}

// processReused reports that pid is now a different process than started.
// A missing observation does not count as reuse.
func processReused(pid int, started time.Time) bool {
	observed, ok := ObservedStartTime(pid)
	if !ok {
		return false
	}
	same, known := SameProcess(
		ProcessIdentity{PID: pid, Start: started},
		ProcessIdentity{PID: pid, Start: observed},
	)
	return known && !same
}

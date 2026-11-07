//go:build windows

package safety

import "fmt"

// windowsBackend refuses danger and confined launches.
// Redirecting HOME, USERPROFILE, APPDATA, or TEMP is not OS write isolation.
// Junction and reparse cleanup is BLOCKED on this platform.
type windowsBackend struct{}

func (windowsBackend) Name() string { return "windows-unavailable" }

func (windowsBackend) Available() error {
	return fmt.Errorf("%w: Windows write isolation and reparse-point cleanup are BLOCKED", ErrBackendUnavailable)
}

func platformBackend() Backend { return windowsBackend{} }

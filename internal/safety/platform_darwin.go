//go:build darwin

package safety

import "fmt"

// darwinBackend is implemented as a seatbelt profile builder plus a hard stop.
// Native verification on Darwin (existing sentinel denied, project write
// allowed, Mach/securityd denied) has not been run. Available therefore
// fails closed instead of launching sandbox-exec and calling it verified.
type darwinBackend struct{}

func (darwinBackend) Name() string { return "darwin-sandbox-exec" }

func (darwinBackend) Available() error {
	return fmt.Errorf("%w: darwin sandbox-exec and Mach/securityd credential IPC are BLOCKED pending native verification", ErrBackendUnavailable)
}

func platformBackend() Backend { return darwinBackend{} }

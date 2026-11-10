//go:build !linux && !darwin && !windows

package safety

import (
	"fmt"
	"runtime"
)

type unsupportedBackend struct{}

func (unsupportedBackend) Name() string { return "unsupported" }

func (unsupportedBackend) Available() error {
	return fmt.Errorf("%w: %s has no verified write isolation backend (BLOCKED)", ErrBackendUnavailable, runtime.GOOS)
}

func platformBackend() Backend { return unsupportedBackend{} }

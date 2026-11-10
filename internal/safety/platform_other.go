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

func defaultDarwinImageInspector(exePath string) (canonicalPath string, dev, ino uint64, isReg bool, sha256Hex string, err error) {
	return "", 0, 0, false, "", fmt.Errorf("%w: darwin image inspector unavailable on this platform", ErrRefused)
}

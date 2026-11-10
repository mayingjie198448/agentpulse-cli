//go:build windows

package safety

import "fmt"

// appContainerBackend is a LowBox AppContainer with zero capabilities.
// Available only means the documented userenv/kernel32 entry points load.
// It does not mean a native fixture has passed. Cross-compiling this package
// is not a Windows pass. Prepare still refuses before the user command if
// profile creation or the fresh sentinel probe fails.
type appContainerBackend struct{}

func (appContainerBackend) Name() string { return "windows-appcontainer" }

func (appContainerBackend) Available() error {
	if err := appContainerProcs(); err != nil {
		return fmt.Errorf("%w: %s", ErrBackendUnavailable, err)
	}
	return nil
}

func platformBackend() Backend { return appContainerBackend{} }

func defaultDarwinImageInspector(exePath string) (canonicalPath string, dev, ino uint64, isReg bool, sha256Hex string, err error) {
	return "", 0, 0, false, "", fmt.Errorf("%w: darwin image inspector unavailable on this platform", ErrRefused)
}

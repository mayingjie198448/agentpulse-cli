//go:build darwin

package safety

import (
	"fmt"
	"os"
)

const sandboxExecPath = "/usr/bin/sandbox-exec"

// darwinBackend implements OS write isolation using macOS Seatbelt (/usr/bin/sandbox-exec).
// Available verifies that /usr/bin/sandbox-exec is present and executable.
// A nil error is not permission to skip the fresh child probe.
type darwinBackend struct{}

func (darwinBackend) Name() string { return "darwin-sandbox-exec" }

func (darwinBackend) Available() error {
	st, err := os.Stat(sandboxExecPath)
	if err != nil {
		return fmt.Errorf("%w: /usr/bin/sandbox-exec is missing: %v", ErrBackendUnavailable, err)
	}
	if st.Mode()&0111 == 0 {
		return fmt.Errorf("%w: /usr/bin/sandbox-exec is not executable", ErrBackendUnavailable)
	}
	return nil
}

func platformBackend() Backend { return darwinBackend{} }

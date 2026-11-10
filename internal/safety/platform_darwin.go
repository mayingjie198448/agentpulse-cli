//go:build darwin

package safety

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
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

func defaultDarwinImageInspector(exePath string) (canonicalPath string, dev, ino uint64, isReg bool, sha256Hex string, err error) {
	fd, st, err := openFileNoFollow(exePath)
	if err != nil {
		return "", 0, 0, false, "", err
	}
	defer unix.Close(fd)

	isReg = (st.Mode & unix.S_IFMT) == unix.S_IFREG
	dev, ino = ident(st)
	canon, err := fdPath(fd)
	if err != nil {
		return "", 0, 0, false, "", err
	}

	h := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		n, rerr := unix.Read(fd, buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr != nil {
			if rerr == unix.EINTR {
				continue
			}
			return "", 0, 0, false, "", rerr
		}
		if n == 0 {
			break
		}
	}
	hashHex := hex.EncodeToString(h.Sum(nil))
	return canon, dev, ino, isReg, hashHex, nil
}

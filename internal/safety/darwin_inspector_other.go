//go:build !darwin

package safety

import "fmt"

// defaultDarwinImageInspector is the non-Darwin binding for the inspector
// referenced by untagged darwin_logic.go. Every non-Darwin OS, including
// Linux, refuses. This does not add a Linux, Windows, or generic isolation
// backend; the real FD inspector stays in the Darwin file.
func defaultDarwinImageInspector(exePath string) (canonicalPath string, dev, ino uint64, isReg bool, sha256Hex string, err error) {
	return "", 0, 0, false, "", fmt.Errorf("%w: darwin image inspector unavailable on this platform", ErrRefused)
}

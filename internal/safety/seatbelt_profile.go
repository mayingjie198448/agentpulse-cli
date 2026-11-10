package safety

import (
	"fmt"
	"strings"
)

// DarwinSeatbeltProfile generates the Seatbelt profile for a run root.
// Generating the profile is not a verification result. darwinBackend.Available
// checks /usr/bin/sandbox-exec presence and execution bit, while runtime
// confinement requires the child probe to verify write isolation.
// Mach/securityd IPC and keychain confidentiality remain unverified and blocked.
func DarwinSeatbeltProfile(root string) (string, error) {
	if err := darwinPathShapeError(root); err != nil {
		return "", err
	}
	if strings.ContainsAny(root, "()\"\\") {
		return "", fmt.Errorf("%w: run root cannot be embedded in a seatbelt profile", ErrRefused)
	}
	return fmt.Sprintf(`(version 1)
(deny default)
(allow process-exec)
(allow process-fork)
(allow signal (target self))
(allow file-read*)
(allow file-ioctl (literal "/dev/null"))
(allow file-write* (literal "/dev/null"))
(deny file-write*)
(allow file-write* (subpath %q))
(deny mach-lookup (global-name "com.apple.SecurityServer"))
(deny mach-lookup (global-name "com.apple.securityd"))
(deny mach-lookup (global-name "com.apple.ocspd"))
`, root), nil
}

func darwinPathShapeError(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("%w: empty path", ErrCleanupBlocked)
	}
	if trimmed != path {
		return fmt.Errorf("%w: whitespace path", ErrCleanupBlocked)
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%w: relative path", ErrCleanupBlocked)
	}
	if path == "/" {
		return fmt.Errorf("%w: refuse /", ErrCleanupBlocked)
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("%w: refuse component %q", ErrCleanupBlocked, part)
		}
	}
	return nil
}

package safety

import (
	"fmt"
	"path"
	"strings"
)

// DarwinSeatbeltProfile generates the Seatbelt profile for a run root.
// Generating the profile is not a verification result. darwinBackend.Available
// checks /usr/bin/sandbox-exec presence and execution bit, while runtime
// confinement requires the child probe to verify write and read isolation.
// Mach/securityd IPC and keychain confidentiality remain unverified and blocked.
func DarwinSeatbeltProfile(root string, allowedReadLiterals ...string) (string, error) {
	if err := darwinPathShapeError(root); err != nil {
		return "", err
	}
	if strings.ContainsAny(root, "()\"\\") {
		return "", fmt.Errorf("%w: run root cannot be embedded in a seatbelt profile", ErrRefused)
	}

	var extraReads strings.Builder
	for _, lit := range allowedReadLiterals {
		if err := validateDarwinReadableLiteral(lit, root); err != nil {
			return "", err
		}
		clean := path.Clean(lit)
		fmt.Fprintf(&extraReads, "(allow file-read* (literal %q))\n", clean)
	}

	return fmt.Sprintf(`(version 1)
(deny default)
(allow process-exec)
(allow process-fork)
(allow signal (target self))
(deny file-write*)
(allow file-write* (subpath %q))
(allow file-write* (literal "/dev/null"))
(deny file-read*)
(allow file-read* (subpath %q))
(allow file-read* (subpath "/System/Library"))
(allow file-read* (subpath "/usr/lib"))
(allow file-read* (subpath "/usr/share"))
(allow file-read* (subpath "/private/var/db/dyld"))
(allow file-read* (subpath "/bin"))
(allow file-read* (subpath "/sbin"))
(allow file-read* (subpath "/usr/bin"))
(allow file-read* (subpath "/Library/Preferences/Logging"))
(allow file-read* (literal "/dev/null"))
(allow file-read* (literal "/dev/zero"))
(allow file-read* (literal "/dev/urandom"))
(allow file-read* (literal "/dev/random"))
(allow file-read* (literal "/dev/dtracehelper"))
(allow file-ioctl (literal "/dev/null"))
%s(deny mach-lookup (global-name "com.apple.SecurityServer"))
(deny mach-lookup (global-name "com.apple.securityd"))
(deny mach-lookup (global-name "com.apple.ocspd"))
(deny file-read* (subpath "/Users"))
(deny file-read* (subpath "/System/Volumes/Data/Users"))
(deny file-read* (subpath "/Library/Keychains"))
(deny file-read* (subpath "/private/var/root"))
(deny file-read* (subpath "/private/var/db/dslocal"))
(deny file-write* (subpath "/Users"))
(deny file-write* (subpath "/System/Volumes/Data/Users"))
(deny file-write* (subpath "/Library/Keychains"))
`, root, root, extraReads.String()), nil
}

func validateDarwinReadableLiteral(lit string, runRoot string) error {
	trimmed := strings.TrimSpace(lit)
	if trimmed == "" {
		return fmt.Errorf("%w: empty read literal", ErrRefused)
	}
	if err := darwinPathShapeError(lit); err != nil {
		return fmt.Errorf("%w: invalid read literal path shape: %v", ErrRefused, err)
	}
	if strings.ContainsAny(lit, "()\"\\") {
		return fmt.Errorf("%w: read literal contains injection characters", ErrRefused)
	}
	cleanLit := path.Clean(lit)
	for _, forbidden := range []string{
		"/Users",
		"/System/Volumes/Data/Users",
		"/Library/Keychains",
		"/private/var/root",
		"/private/var/db/dslocal",
	} {
		if withinPath(forbidden, cleanLit) || cleanLit == forbidden {
			return fmt.Errorf("%w: read literal %q is inside protected path %q", ErrRefused, cleanLit, forbidden)
		}
	}
	for _, sensitive := range []string{
		".ssh",
		"Keychains",
		"login.keychain",
		"login.keychain-db",
		"credentials",
		".config",
		".aws",
		".gnupg",
	} {
		for _, part := range strings.Split(cleanLit, "/") {
			if part == sensitive {
				return fmt.Errorf("%w: read literal %q contains sensitive segment %q", ErrRefused, cleanLit, sensitive)
			}
		}
	}
	return nil
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

//go:build darwin

package safety

import "golang.org/x/sys/unix"

func getDarwinFileIdentity(path string) (DarwinTargetIdentity, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return DarwinTargetIdentity{}, err
	}
	dev, ino := ident(st)
	return DarwinTargetIdentity{Dev: dev, Ino: ino}, nil
}

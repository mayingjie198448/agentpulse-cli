//go:build darwin

package safety

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func getDarwinFileIdentity(path string) (DarwinTargetIdentity, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return DarwinTargetIdentity{}, err
	}
	dev, ino := ident(st)
	if dev == 0 || ino == 0 {
		return DarwinTargetIdentity{}, fmt.Errorf("zero file identity for %q (dev=%d ino=%d)", path, dev, ino)
	}
	return DarwinTargetIdentity{Dev: dev, Ino: ino}, nil
}

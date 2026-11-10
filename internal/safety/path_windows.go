//go:build windows

package safety

import (
	"fmt"

	"golang.org/x/sys/windows"
)

const (
	fileNameOpened = 0x8
	// deleteAccess is DELETE (0x00010000) from winnt.h.
	deleteAccess = 0x00010000
)

type winFileID struct {
	volume  uint64
	index   uint64
	dir     bool
	reparse bool
}

func openMeta(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		p,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func fileID(h windows.Handle) (winFileID, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return winFileID{}, err
	}
	return winFileID{
		volume:  uint64(info.VolumeSerialNumber),
		index:   uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		dir:     info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
		reparse: info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0,
	}, nil
}

func handlePath(h windows.Handle) (string, error) {
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), fileNameOpened)
	if err != nil {
		return "", err
	}
	if n == 0 || int(n) >= len(buf) {
		return "", fmt.Errorf("kernel path truncated")
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func statNoFollow(path string) (string, winFileID, error) {
	if err := pathShapeError(path); err != nil {
		return "", winFileID{}, err
	}
	h, err := openMeta(path, windows.FILE_READ_ATTRIBUTES|deleteAccess)
	if err != nil {
		return "", winFileID{}, err
	}
	defer windows.CloseHandle(h)
	id, err := fileID(h)
	if err != nil {
		return "", winFileID{}, err
	}
	if id.reparse {
		return "", id, fmt.Errorf("%w: reparse point %s", ErrCleanupBlocked, path)
	}
	kpath, err := handlePath(h)
	if err != nil {
		return "", id, err
	}
	return kpath, id, nil
}

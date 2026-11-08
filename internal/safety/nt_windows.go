//go:build windows

package safety

import (
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fileNamesInformation    = 12
	fileOpen                = 1
	fileCreate              = 2
	fileDirectoryFile       = 0x00000001
	fileNonDirectoryFile    = 0x00000040
	fileOpenReparsePoint    = 0x00200000
	fileSynchronousNonAlert = 0x00000020
	objCaseInsensitive      = 0x00000040
)

var (
	modNtdll                 = windows.NewLazySystemDLL("ntdll.dll")
	procNtQueryDirectoryFile = modNtdll.NewProc("NtQueryDirectoryFile")
	procNtCreateFile         = modNtdll.NewProc("NtCreateFile")
)

type ioStatusBlock struct {
	Status      uintptr
	Information uintptr
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type objectAttributes struct {
	Length                   uint32
	RootDirectory            windows.Handle
	ObjectName               *unicodeString
	Attributes               uint32
	SecurityDescriptor       uintptr
	SecurityQualityOfService uintptr
}

type fileNamesHeader struct {
	NextEntryOffset uint32
	FileIndex       uint32
	FileNameLength  uint32
}

// listHandleNames enumerates a directory through its handle. It does not
// use a path, so a junction swapped under that path is not followed.
func listHandleNames(h windows.Handle) ([]string, error) {
	if err := procNtQueryDirectoryFile.Find(); err != nil {
		return nil, fmt.Errorf("BLOCKED: missing ntdll!NtQueryDirectoryFile (%v)", err)
	}
	var names []string
	buf := make([]byte, 64*1024)
	restart := uintptr(1)
	for {
		var ios ioStatusBlock
		r, _, _ := procNtQueryDirectoryFile.Call(
			uintptr(h), 0, 0, 0,
			uintptr(unsafe.Pointer(&ios)),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
			fileNamesInformation,
			0,
			0,
			restart,
		)
		restart = 0
		st, err := ntCallStatus(r, ios)
		if err != nil {
			return nil, err
		}
		usable, err := ntQueryResult(st, ios.Information, len(buf))
		if errors.Is(err, errNTNoMore) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		offset := 0
		for offset+int(unsafe.Sizeof(fileNamesHeader{})) <= usable {
			hdr := (*fileNamesHeader)(unsafe.Pointer(&buf[offset]))
			nameBytes := int(hdr.FileNameLength)
			nameStart := offset + int(unsafe.Sizeof(fileNamesHeader{}))
			nameEnd := nameStart + nameBytes
			if nameBytes < 0 || nameBytes%2 != 0 || nameEnd > usable {
				return nil, fmt.Errorf("BLOCKED: directory entry outside NT information length")
			}
			chars := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[nameStart])), nameBytes/2)
			name := string(utf16.Decode(chars))
			if name != "." && name != ".." && name != "" {
				names = append(names, name)
			}
			if hdr.NextEntryOffset == 0 {
				break
			}
			next := offset + int(hdr.NextEntryOffset)
			if hdr.NextEntryOffset < uint32(unsafe.Sizeof(fileNamesHeader{})) || next > usable {
				return nil, fmt.Errorf("BLOCKED: directory link outside NT information length")
			}
			offset = next
		}
	}
}

func openRelative(parent windows.Handle, name string, access uint32) (windows.Handle, error) {
	return ntCreate(parent, name, access, fileOpen, fileOpenReparsePoint|fileSynchronousNonAlert, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
}

func mkdirRelative(parent windows.Handle, name string) error {
	h, err := ntCreate(parent, name, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, fileCreate, fileDirectoryFile|fileOpenReparsePoint|fileSynchronousNonAlert, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

func readRelative(parent windows.Handle, name string) ([]byte, error) {
	h, err := openRelative(parent, name, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	id, err := fileID(h)
	if err != nil || id.reparse || id.dir {
		return nil, fmt.Errorf("BLOCKED: relative file is a directory or reparse point")
	}
	dup, err := duplicate(h)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), name)
	defer f.Close()
	return io.ReadAll(f)
}

// ntCallStatus fails closed when the syscall return and IO_STATUS_BLOCK disagree
// or either one is STATUS_PENDING.
func ntCallStatus(ret uintptr, ios ioStatusBlock) (uint32, error) {
	st := uint32(ret)
	ioSt := uint32(ios.Status)
	if st == statusPending || ioSt == statusPending {
		return 0, fmt.Errorf("BLOCKED: NT I/O still pending")
	}
	if st != 0 && ioSt != 0 && st != ioSt {
		return 0, fmt.Errorf("BLOCKED: NT status mismatch return=0x%08X ios=0x%08X", st, ioSt)
	}
	if st == 0 {
		st = ioSt
	}
	return st, nil
}

func ntCreate(parent windows.Handle, name string, access, disposition, options, share uint32) (windows.Handle, error) {
	if err := procNtCreateFile.Find(); err != nil {
		return 0, fmt.Errorf("BLOCKED: missing ntdll!NtCreateFile (%v)", err)
	}
	u, err := windows.UTF16FromString(name)
	if err != nil {
		return 0, err
	}
	if len(u) == 0 {
		return 0, fmt.Errorf("empty child name")
	}
	us := unicodeString{
		Length:        uint16((len(u) - 1) * 2),
		MaximumLength: uint16(len(u) * 2),
		Buffer:        &u[0],
	}
	oa := objectAttributes{
		Length:        uint32(unsafe.Sizeof(objectAttributes{})),
		RootDirectory: parent,
		ObjectName:    &us,
		Attributes:    objCaseInsensitive,
	}
	var ios ioStatusBlock
	var h windows.Handle
	access = accessForSyncNT(access)
	r, _, _ := procNtCreateFile.Call(
		uintptr(unsafe.Pointer(&h)),
		uintptr(access),
		uintptr(unsafe.Pointer(&oa)),
		uintptr(unsafe.Pointer(&ios)),
		0,
		0,
		uintptr(share),
		uintptr(disposition),
		uintptr(options|fileSynchronousNonAlert),
		0,
		0,
	)
	st, err := ntCallStatus(r, ios)
	if err != nil {
		return 0, err
	}
	if err := ntCreateStatus(st); err != nil {
		return 0, err
	}
	if h == 0 {
		return 0, fmt.Errorf("BLOCKED: NtCreateFile returned a zero handle")
	}
	return h, nil
}

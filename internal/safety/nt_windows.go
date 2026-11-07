//go:build windows

package safety

import (
	"fmt"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fileNamesInformation = 12
	fileOpen             = 1
	fileOpenReparsePoint = 0x00200000
	fileDirectoryFile    = 0x00000001
	objCaseInsensitive   = 0x00000040
	statusNoMoreFiles    = 0x80000006
	statusBufferOverflow = 0x80000005
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
		status := uint32(r)
		if status == statusNoMoreFiles {
			return names, nil
		}
		if int32(r) < 0 && status != statusBufferOverflow {
			return names, fmt.Errorf("NtQueryDirectoryFile status 0x%08X", status)
		}
		offset := 0
		for {
			hdr := (*fileNamesHeader)(unsafe.Pointer(&buf[offset]))
			n := int(hdr.FileNameLength) / 2
			chars := unsafe.Slice((*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(&buf[offset]))+unsafe.Sizeof(*hdr))), n)
			name := string(utf16.Decode(chars))
			if name != "." && name != ".." && name != "" {
				names = append(names, name)
			}
			if hdr.NextEntryOffset == 0 {
				break
			}
			offset += int(hdr.NextEntryOffset)
			if offset < 0 || offset >= len(buf) {
				return names, fmt.Errorf("directory listing truncated")
			}
		}
		if status != statusBufferOverflow && int32(r) >= 0 {
			// A short buffer asks for another call. A full success ends the scan
			// only when the next call returns STATUS_NO_MORE_FILES.
		}
	}
}

func openRelative(parent windows.Handle, name string, access uint32) (windows.Handle, error) {
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
	r, _, _ := procNtCreateFile.Call(
		uintptr(unsafe.Pointer(&h)),
		uintptr(access),
		uintptr(unsafe.Pointer(&oa)),
		uintptr(unsafe.Pointer(&ios)),
		0,
		0,
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		fileOpen,
		fileOpenReparsePoint,
		0,
		0,
	)
	if int32(r) < 0 {
		return 0, fmt.Errorf("NtCreateFile status 0x%08X", uint32(r))
	}
	return h, nil
}

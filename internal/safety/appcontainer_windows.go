//go:build windows

package safety

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows confinement is a LowBox AppContainer (userenv CreateAppContainerProfile
// plus PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES) with CapabilityCount 0.
//
// Prerequisites, not enabled by this package:
//   - Windows 8 or newer userenv exports: CreateAppContainerProfile,
//     DeriveAppContainerSidFromAppContainerName, GetAppContainerFolderPath,
//     DeleteAppContainerProfile.
//   - kernel32 exports: InitializeProcThreadAttributeList, UpdateProcThreadAttribute,
//     DeleteProcThreadAttributeList.
//   - The process token is not adjusted. No privilege is enabled. No optional
//     Windows feature is turned on. No administrator token is required or created.
//
// Write root: the per-run package folder returned by GetAppContainerFolderPath.
// Windows creates that folder and its DACL for the new AppContainer SID. This
// package does not rewrite the user profile, Documents, Desktop, or .ssh DACLs.
// The only optional DACL edit is a file-only RX ACE on the single executable
// about to be launched, and only when that file already lives under os.TempDir
// or the fixture base. The ACE is revoked after the run. If the image is
// elsewhere and the container cannot read it, launch fails closed.
//
// Not claimed: credential-broker IPC (DPAPI, wincred, ssh-agent), a complete
// network proof, or junction behavior beyond the reparse-point checks below.
// Network capabilities are not granted (no internetClient). A Job Object is
// not used as a filesystem sandbox. HOME, USERPROFILE, and cwd are not isolation.
//
// Native PASS is BLOCKED until `agentpulse safety fixture` is executed on
// Windows. Cross-compilation does not satisfy that.

var (
	modUserenv  = windows.NewLazySystemDLL("userenv.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procCreateAppContainerProfile                 = modUserenv.NewProc("CreateAppContainerProfile")
	procDeleteAppContainerProfile                 = modUserenv.NewProc("DeleteAppContainerProfile")
	procDeriveAppContainerSidFromAppContainerName = modUserenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	procGetAppContainerFolderPath                 = modUserenv.NewProc("GetAppContainerFolderPath")
	procInitializeProcThreadAttributeList         = modKernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute                 = modKernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList             = modKernel32.NewProc("DeleteProcThreadAttributeList")
)

const (
	procThreadAttributeSecurityCapabilities = 0x20009
	extendedStartupInfoPresent              = 0x00080000
	createUnicodeEnvironment                = 0x00000400
	startfUseStdHandles                     = 0x00000100
	hresultAlreadyExists                    = 0x800700B7
)

type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    uintptr
	CapabilityCount uint32
	Reserved        uint32
}

type startupInfoEx struct {
	StartupInfo   windows.StartupInfo
	AttributeList *byte
}

func appContainerProcs() error {
	procs := []struct {
		dll  string
		proc *windows.LazyProc
	}{
		{"userenv.dll!CreateAppContainerProfile", procCreateAppContainerProfile},
		{"userenv.dll!DeleteAppContainerProfile", procDeleteAppContainerProfile},
		{"userenv.dll!DeriveAppContainerSidFromAppContainerName", procDeriveAppContainerSidFromAppContainerName},
		{"userenv.dll!GetAppContainerFolderPath", procGetAppContainerFolderPath},
		{"kernel32.dll!InitializeProcThreadAttributeList", procInitializeProcThreadAttributeList},
		{"kernel32.dll!UpdateProcThreadAttribute", procUpdateProcThreadAttribute},
		{"kernel32.dll!DeleteProcThreadAttributeList", procDeleteProcThreadAttributeList},
	}
	for _, p := range procs {
		if err := p.proc.Find(); err != nil {
			return fmt.Errorf("BLOCKED: missing %s (%v)", p.dll, err)
		}
	}
	return nil
}

func hrErr(name string, r uintptr) error {
	if r == 0 {
		return nil
	}
	return fmt.Errorf("BLOCKED: %s HRESULT 0x%08X", name, uint32(r))
}

type appProfile struct {
	name   string
	sid    *windows.SID
	sidStr string
	folder string
}

func createAppProfile(runID string) (appProfile, error) {
	if err := appContainerProcs(); err != nil {
		return appProfile{}, err
	}
	name := "ap" + runID
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return appProfile{}, err
	}
	display, _ := windows.UTF16PtrFromString("agentpulse-run")
	var sid *windows.SID
	r, _, _ := procCreateAppContainerProfile.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(display)),
		uintptr(unsafe.Pointer(display)),
		0, 0,
		uintptr(unsafe.Pointer(&sid)),
	)
	if uint32(r) == hresultAlreadyExists || sid == nil && r != 0 {
		if derr := deriveSID(namePtr, &sid); derr != nil && r != 0 && uint32(r) != hresultAlreadyExists {
			return appProfile{}, hrErr("CreateAppContainerProfile", r)
		}
		if sid == nil {
			if r != 0 && uint32(r) != hresultAlreadyExists {
				return appProfile{}, hrErr("CreateAppContainerProfile", r)
			}
			return appProfile{}, fmt.Errorf("BLOCKED: CreateAppContainerProfile did not return a SID")
		}
	} else if r != 0 {
		return appProfile{}, hrErr("CreateAppContainerProfile", r)
	}
	sidStr, err := sidString(sid)
	if err != nil {
		windows.LocalFree(windows.Handle(unsafe.Pointer(sid)))
		_ = deleteAppProfile(name)
		return appProfile{}, err
	}
	folder, err := packageFolder(sidStr)
	if err != nil {
		windows.LocalFree(windows.Handle(unsafe.Pointer(sid)))
		_ = deleteAppProfile(name)
		return appProfile{}, err
	}
	return appProfile{name: name, sid: sid, sidStr: sidStr, folder: folder}, nil
}

func deriveSID(name *uint16, sid **windows.SID) error {
	r, _, _ := procDeriveAppContainerSidFromAppContainerName.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(sid)))
	return hrErr("DeriveAppContainerSidFromAppContainerName", r)
}

func sidString(sid *windows.SID) (string, error) {
	var raw *uint16
	if err := windows.ConvertSidToStringSid(sid, &raw); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(raw)))
	return windows.UTF16PtrToString(raw), nil
}

func packageFolder(sidStr string) (string, error) {
	ptr, err := windows.UTF16PtrFromString(sidStr)
	if err != nil {
		return "", err
	}
	var path *uint16
	r, _, _ := procGetAppContainerFolderPath.Call(uintptr(unsafe.Pointer(ptr)), uintptr(unsafe.Pointer(&path)))
	if err := hrErr("GetAppContainerFolderPath", r); err != nil {
		return "", err
	}
	if path == nil {
		return "", fmt.Errorf("BLOCKED: GetAppContainerFolderPath returned an empty path")
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(path))
	return windows.UTF16PtrToString(path), nil
}

func deleteAppProfile(name string) error {
	if stringsTrim(name) == "" {
		return nil
	}
	if err := procDeleteAppContainerProfile.Find(); err != nil {
		return fmt.Errorf("BLOCKED: missing userenv.dll!DeleteAppContainerProfile (%v)", err)
	}
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	r, _, _ := procDeleteAppContainerProfile.Call(uintptr(unsafe.Pointer(ptr)))
	return hrErr("DeleteAppContainerProfile", r)
}

func stringsTrim(s string) string { return strings.TrimSpace(s) }

func freeSID(sid *windows.SID) {
	if sid != nil {
		windows.LocalFree(windows.Handle(unsafe.Pointer(sid)))
	}
}

//go:build windows

package safety

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func noNewPrivsValue() string { return "0" }

func confinementNote() string {
	if inAppContainer() {
		return "appcontainer=1\n"
	}
	return "appcontainer=0\n"
}

func inAppContainer() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()
	var flag uint32
	var n uint32
	const tokenIsAppContainer = 29
	err = windows.GetTokenInformation(token, tokenIsAppContainer, (*byte)(unsafe.Pointer(&flag)), uint32(unsafe.Sizeof(flag)), &n)
	return err == nil && flag != 0
}

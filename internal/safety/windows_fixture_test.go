//go:build windows

package safety

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	MaybeHandleInternal(os.Args)
	MaybeFixtureWorker(os.Args)
	os.Exit(m.Run())
}

// TestWindowsNativeFixture is the ops acceptance command on a Windows machine:
//
//	go test -c -o %TEMP%\agentpulse-safety-windows.test ./internal/safety/
//	%TEMP%\agentpulse-safety-windows.test -test.run TestWindowsNativeFixture -test.v
//
// It does not launch agy. Cross-compiling the test is not a pass.
func TestWindowsNativeFixture(t *testing.T) {
	report, err := RunNativeFixture(t.Context())
	t.Log(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "result=PASS") || !strings.Contains(report, "appcontainer=1") {
		t.Fatalf("report:\n%s", report)
	}
	if !strings.Contains(report, "BLOCKED_UNTIL_OPS_EXECUTES") {
		t.Fatalf("catalog flipped to a compile-time pass:\n%s", report)
	}
}

// TestWindowsJunctionDeleteRetainsTarget is a native negative test.
// It is not executed by Linux cross-compilation.
func TestWindowsJunctionDeleteRetainsTarget(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "jump")
	if err := os.Mkdir(link, 0700); err != nil {
		t.Fatal(err)
	}
	if err := setMountPoint(link, outside); err != nil {
		t.Fatalf("cannot create junction: %v", err)
	}
	kpath, id, err := statNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	rec := RunRecord{
		RunID: "0123456789abcdef0123456789abcdef",
		Root:  kpath, Dev: id.volume, Ino: id.index, Nonce: "nonce",
	}
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, markerName), body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteOwned(rec, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "keep" {
		t.Fatalf("junction target changed: %v %q", err, got)
	}
}

func setMountPoint(link, target string) error {
	h, err := openMeta(link, windows.GENERIC_WRITE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	subst, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	buf, err := mountPointReparse(subst, printName)
	if err != nil {
		return err
	}
	var returned uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &returned, nil)
}

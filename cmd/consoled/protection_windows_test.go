package main

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func TestWindowsDaemonRefusesBeforeOpeningState(t *testing.T) {
	home, before := windowsUnsafeDaemonHome(t)
	paths := runtime.Paths{Home: home, Auth: filepath.Join(home, "auth.secret"), Lock: filepath.Join(home, "consoled.lock"), DB: filepath.Join(home, "console.db"), Logs: filepath.Join(home, "logs"), Listen: "127.0.0.1:0"}
	if err := runDaemon(paths); err == nil || err.Error() != "private Windows DACL must grant only the current user" {
		t.Fatalf("daemon protection refusal: %v", err)
	}
	after, err := windows.GetNamedSecurityInfo(home, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || after.String() != before {
		t.Fatalf("daemon changed unsafe home security: %v %v", after, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("daemon wrote state before refusal: %v %v", entries, err)
	}
}

func windowsUnsafeDaemonHome(t *testing.T) (string, string) {
	t.Helper()
	parent := testfixture.PrivateHome(t)
	dir, err := runtime.OpenPrivateDirectory(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	}()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.NewNTUnicodeString("unsafe-home")
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	options := uint32(windows.FILE_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err := windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_CREATE, options, 0, 0); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	}()
	actual, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || actual.String() != descriptor.String() {
		t.Fatalf("actual fresh unsafe descriptor=%v expected=%v err=%v", actual, descriptor, err)
	}
	t.Logf("actual at-birth unsafe home descriptor=%s", actual.String())
	return filepath.Join(parent, "unsafe-home"), actual.String()
}

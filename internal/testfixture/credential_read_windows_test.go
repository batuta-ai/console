package testfixture_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func credentialReadInfo(t *testing.T, path string) (windows.ByHandleFileInformation, *windows.SECURITY_DESCRIPTOR) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Error(err)
		}
	}()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return info, sd
}

func credentialReadPrivileges(t *testing.T) []byte {
	t.Helper()
	var size uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenPrivileges, nil, 0, &size)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size == 0 {
		t.Fatal("privilege size", size, err)
	}
	data := make([]byte, size)
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenPrivileges, &data[0], size, &size); err != nil {
		t.Fatal(err)
	}
	return data
}

func newCredentialReadFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testfixture.PrivateHome(t), "credential.json")
	f, err := consolert.CreatePrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write([]byte("synthetic-never-real"))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeCredentialWorldReadableFixture(t *testing.T) {
	path := newCredentialReadFile(t)
	before, beforeSD := credentialReadInfo(t, path)
	privileges := credentialReadPrivileges(t)
	if err := testfixture.MakeCredentialWorldReadable(path); err != nil {
		t.Fatal(err)
	}
	after, sd := credentialReadInfo(t, path)
	if after.VolumeSerialNumber != before.VolumeSerialNumber || after.FileIndexHigh != before.FileIndexHigh || after.FileIndexLow != before.FileIndexLow || after.NumberOfLinks != 1 || after.FileAttributes != before.FileAttributes {
		t.Fatal("owned file identity, links or attributes changed", before, after)
	}
	oldOwner, _, err := beforeSD.Owner()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || !windows.EqualSid(owner, oldOwner) {
		t.Fatal("owner changed", err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("fixture DACL not protected", control, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		t.Fatal("actual World read grant missing", sd.String(), err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	seenUser, seenWorld := false, false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			t.Fatal("unexpected ACE type/flags", ace.Header)
		}
		switch {
		case windows.EqualSid(sid, user.User.Sid):
			seenUser = true
			if uint32(ace.Mask) != windows.STANDARD_RIGHTS_REQUIRED|windows.SYNCHRONIZE|0x1ff {
				t.Fatal("current user grant differs", ace.Mask)
			}
		case sid.IsWellKnown(windows.WinWorldSid):
			seenWorld = true
			if uint32(ace.Mask) != windows.FILE_GENERIC_READ {
				t.Fatal("World read grant differs", ace.Mask)
			}
		default:
			t.Fatal("foreign unexpected ACE", sid.String())
		}
	}
	if !seenUser || !seenWorld {
		t.Fatal("actual principals missing", sd.String())
	}
	if f, err := consolert.OpenPrivateFile(path); err == nil {
		f.Close()
		t.Fatal("unsafe credential accepted as private")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "synthetic-never-real" {
		t.Fatal("credential content changed", err)
	}
	if !bytes.Equal(privileges, credentialReadPrivileges(t)) {
		t.Fatal("process privileges changed")
	}
	t.Logf("actual owned World-readable credential descriptor=%s identity=%d:%d:%d bytes unchanged, private validator refused", sd.String(), after.VolumeSerialNumber, after.FileIndexHigh, after.FileIndexLow)
}

func TestNativeCredentialWorldReadableRefusesLinksAndDirectories(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			target := newCredentialReadFile(t)
			path := filepath.Join(filepath.Dir(target), "candidate")
			switch kind {
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := testfixture.MakePrivateDirectory(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, sd := credentialReadInfo(t, target)
			if err := testfixture.MakeCredentialWorldReadable(path); err == nil {
				t.Error("unsafe object accepted", kind)
			}
			after, afterSD := credentialReadInfo(t, target)
			if before.FileAttributes != after.FileAttributes || before.NumberOfLinks != after.NumberOfLinks || before.FileIndexHigh != after.FileIndexHigh || before.FileIndexLow != after.FileIndexLow || sd.String() != afterSD.String() {
				t.Fatal("unsafe object target changed", kind)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "synthetic-never-real" {
				t.Fatal("unsafe target bytes changed", err)
			}
		})
	}
}

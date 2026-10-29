package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func credentialFixtureDACL(t *testing.T, path, descriptor string, protection windows.SECURITY_INFORMATION) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatal("synthetic fixture DACL", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|protection, nil, nil, acl, nil); err != nil {
		t.Fatal("synthetic fixture DACL construction", err)
	}
}

func credentialFixtureDirectoryMetadata(t *testing.T, path string) (*windows.SECURITY_DESCRIPTOR, windows.ByHandleFileInformation) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	infoErr := windows.GetFileInformationByHandle(handle, &info)
	sd, sdErr := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err := errors.Join(infoErr, sdErr, windows.CloseHandle(handle)); err != nil {
		t.Fatal(err)
	}
	return sd, info
}

func TestNativeCredentialPrivateDirectoryPredicate(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	for _, kind := range []string{"private", "foreign-read", "unprotected", "wrong-owner", "regular-file", "reparse"} {
		t.Run(kind, func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			// No inheritable ACE on this disposable parent isolates the protection bit.
			credentialFixtureDACL(t, root, "D:P(A;;FA;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			path := filepath.Join(root, "predicate-target")
			switch kind {
			case "regular-file":
				if err := os.WriteFile(path, []byte("owned predicate fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "reparse":
				target := filepath.Join(root, "actual private target")
				if err := testfixture.MakePrivateDirectory(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := testfixture.DirectoryLink(target, path); err != nil {
					t.Fatal(err)
				}
			case "wrong-owner":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				credentialFixtureDACL(t, path, "D:P(A;;FA;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			default:
				if err := testfixture.MakePrivateDirectory(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "foreign-read" {
				credentialFixtureDACL(t, path, "D:P(A;;FA;;;"+sid+")(A;;FR;;;WD)", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			}
			if kind == "unprotected" {
				credentialFixtureDACL(t, path, "D:(A;;FA;;;"+sid+")", windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "regular-file":
				if !info.Mode().IsRegular() || info.IsDir() {
					t.Fatal("actual regular-file fixture type", info.Mode())
				}
			case "reparse":
				_, native := credentialFixtureDirectoryMetadata(t, path)
				if native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 || native.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || !info.IsDir() {
					t.Fatal("actual directory reparse fixture", native.FileAttributes)
				}
			default:
				sd, native := credentialFixtureDirectoryMetadata(t, path)
				owner, _, ownerErr := sd.Owner()
				control, _, controlErr := sd.Control()
				acl, _, aclErr := sd.DACL()
				if err := errors.Join(ownerErr, controlErr, aclErr); err != nil || owner == nil || acl == nil || native.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
					t.Fatal("actual synthetic directory metadata", kind, err)
				}
				if kind == "wrong-owner" {
					if windows.EqualSid(owner, user.User.Sid) || !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
						t.Fatal("actual wrong-owner fixture did not isolate default token owner", owner)
					}
				} else if !windows.EqualSid(owner, user.User.Sid) {
					t.Fatal("unexpected synthetic fixture owner", kind, owner)
				}
				if (control&windows.SE_DACL_PROTECTED != 0) != (kind != "unprotected") {
					t.Fatal("actual synthetic fixture DACL protection", kind, control)
				}
				count := uint16(1)
				if kind == "foreign-read" {
					count = 2
				}
				if acl.AceCount != count {
					t.Fatal("actual synthetic fixture ACE count", kind, acl.AceCount)
				}
				var ace *windows.ACCESS_ALLOWED_ACE
				if err := windows.GetAce(acl, 0, &ace); err != nil {
					t.Fatal(err)
				}
				const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
				if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user.User.Sid) || uint32(ace.Mask)&fullAccess != fullAccess {
					t.Fatal("actual synthetic current-user full-access ACE", kind)
				}
				if kind == "foreign-read" {
					if err := windows.GetAce(acl, 1, &ace); err != nil {
						t.Fatal(err)
					}
					if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).IsWellKnown(windows.WinWorldSid) || uint32(ace.Mask)&windows.FILE_GENERIC_READ != windows.FILE_GENERIC_READ {
						t.Fatal("actual synthetic foreign readable ACE")
					}
				}
				t.Logf("actual fixture kind=%s GoPerm=%04o owner=%s currentSID=%s control=%#x ACEcount=%d", kind, info.Mode().Perm(), owner.String(), sid, control, acl.AceCount)
			}
			if got, want := credentialPrivateDirectory(path, info), kind == "private"; got != want {
				t.Fatalf("actual native directory privacy predicate kind=%s got=%v want=%v", kind, got, want)
			}
		})
	}
}

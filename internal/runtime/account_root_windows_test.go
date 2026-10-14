package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeAccountRootState struct {
	descriptor, owner                       string
	control                                 windows.SECURITY_DESCRIPTOR_CONTROL
	attributes, volume, indexHigh, indexLow uint32
	aceCount                                uint16
}

func nativeAccountRootDACL(t *testing.T, path, descriptor string, protection windows.SECURITY_INFORMATION) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatal("actual root fixture DACL", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|protection, nil, nil, acl, nil); err != nil {
		t.Fatal("set owned root fixture DACL", err)
	}
}

func nativeAccountRootStateAt(t *testing.T, handle windows.Handle, path string) nativeAccountRootState {
	t.Helper()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	if err := nativeAccountRootPathMetadata(handle, path, info); err != nil {
		t.Fatal("actual root path metadata", err)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, ownerErr := sd.Owner()
	control, _, controlErr := sd.Control()
	acl, _, aclErr := sd.DACL()
	if ownerErr != nil || owner == nil || controlErr != nil || aclErr != nil || acl == nil || sd.String() == "" {
		t.Fatal("actual root security metadata", ownerErr, controlErr, aclErr)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		t.Logf("actual account root ACE=%d type=%d SID=%s flags=%#x mask=%#x", i, ace.Header.AceType, sid.String(), ace.Header.AceFlags, ace.Mask)
	}
	state := nativeAccountRootState{descriptor: sd.String(), owner: owner.String(), control: control, attributes: info.FileAttributes, volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow, aceCount: acl.AceCount}
	t.Logf("actual account root path=%s owner=%s control=%#x ACEcount=%d attrs=%#x fileID=%d:%d:%d descriptor=%s", path, state.owner, state.control, state.aceCount, state.attributes, state.volume, state.indexHigh, state.indexLow, state.descriptor)
	return state
}

func TestNativeAccountRootDirectoryPolicy(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	for _, test := range []struct {
		kind string
		mask uint32
	}{
		{"private", 0}, {"private-no-inherit", 0}, {"foreign-read-only", windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE},
		{"foreign-full", windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff},
		{"foreign-add-file", windows.FILE_WRITE_DATA}, {"foreign-add-directory", windows.FILE_APPEND_DATA},
		{"foreign-delete-child", 0x40}, {"foreign-delete", windows.DELETE}, {"foreign-write-ea", windows.FILE_WRITE_EA},
		{"foreign-write-attributes", windows.FILE_WRITE_ATTRIBUTES}, {"foreign-write-dac", windows.WRITE_DAC}, {"foreign-write-owner", windows.WRITE_OWNER},
		{"current-read-only", 0}, {"unprotected", 0}, {"wrong-owner", 0}, {"regular-file", 0}, {"reparse", 0}, {"denied", 0},
	} {
		t.Run(test.kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "owned matrix")
			if err := EnsurePrivateDirectory(root); err != nil {
				t.Fatal(err)
			}
			nativeAccountRootDACL(t, root, "D:P(A;;FA;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			path := filepath.Join(root, "target")
			switch test.kind {
			case "regular-file":
				sd, err := privateWindowsDescriptor(false)
				if err != nil {
					t.Fatal(err)
				}
				sa := windows.SecurityAttributes{SecurityDescriptor: sd}
				sa.Length = uint32(unsafe.Sizeof(sa))
				name, err := windows.UTF16PtrFromString(path)
				if err != nil {
					t.Fatal(err)
				}
				h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.CREATE_NEW, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				f := os.NewFile(uintptr(h), path)
				_, writeErr := f.Write([]byte("owned regular root fixture"))
				if err := errors.Join(writeErr, f.Close()); err != nil {
					t.Fatal(err)
				}
			case "reparse":
				target := filepath.Join(root, "actual private directory")
				if err := EnsurePrivateDirectory(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "wrong-owner":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				nativeAccountRootDACL(t, path, "D:P(A;;FA;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			default:
				if err := EnsurePrivateDirectory(path); err != nil {
					t.Fatal(err)
				}
			}
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal("hold actual owned root metadata", err)
			}
			t.Cleanup(func() {
				if err := windows.CloseHandle(handle); err != nil {
					t.Error(err)
				}
			})
			if test.mask != 0 {
				nativeAccountRootDACL(t, path, fmt.Sprintf("D:P(A;;FA;;;%s)(A;;%#x;;;WD)", sid, test.mask), windows.PROTECTED_DACL_SECURITY_INFORMATION)
			}
			switch test.kind {
			case "private-no-inherit":
				nativeAccountRootDACL(t, path, "D:P(A;;FA;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			case "current-read-only":
				nativeAccountRootDACL(t, path, "D:P(A;;FRFX;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			case "unprotected":
				nativeAccountRootDACL(t, path, "D:(A;;FA;;;"+sid+")", windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
			case "denied":
				nativeAccountRootDACL(t, path, "D:P(D;;FR;;;WD)(A;;FA;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
				t.Cleanup(func() {
					nativeAccountRootDACL(t, path, "D:P(A;;FA;;;"+sid+")", windows.PROTECTED_DACL_SECURITY_INFORMATION)
				})
			}
			before := nativeAccountRootStateAt(t, handle, path)
			if test.kind == "wrong-owner" {
				if before.owner != "S-1-5-32-544" || before.owner == sid {
					t.Fatal("actual foreign default-token owner", before.owner)
				}
			} else if test.kind != "reparse" && before.owner != sid {
				t.Fatal("actual current-user fixture owner", before.owner)
			}
			if (test.kind == "unprotected") != (before.control&windows.SE_DACL_PROTECTED == 0) && test.kind != "reparse" {
				t.Fatal("actual fixture protection state", before.control)
			}
			if test.kind == "regular-file" && before.attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 || test.kind == "reparse" && before.attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
				t.Fatal("actual fixture object kind", before.attributes)
			}
			sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if test.mask != 0 {
				var ace *windows.ACCESS_ALLOWED_ACE
				if acl.AceCount != 2 || windows.GetAce(acl, 1, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || uint32(ace.Mask) != test.mask || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).IsWellKnown(windows.WinWorldSid) {
					t.Fatal("actual foreign fixture mask/SID", test.kind)
				}
			}
			want := test.kind == "private" || test.kind == "private-no-inherit" || test.kind == "foreign-read-only"
			f, openErr := OpenAccountRootDirectory(path)
			if f != nil {
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if (openErr == nil && f != nil) != want || openErr != nil && f != nil {
				t.Errorf("account root Open kind=%s want=%t err=%v", test.kind, want, openErr)
			}
			if after := nativeAccountRootStateAt(t, handle, path); after != before {
				t.Fatal("account root Open mutated fixture metadata")
			}
			ensureErr := EnsureAccountRootDirectory(path)
			if (ensureErr == nil) != want {
				t.Errorf("account root Ensure kind=%s want=%t err=%v", test.kind, want, ensureErr)
			}
			if after := nativeAccountRootStateAt(t, handle, path); after != before {
				t.Fatal("account root Ensure mutated fixture metadata")
			}
			private, privateErr := OpenPrivateDirectory(path)
			if private != nil {
				if err := private.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if (privateErr == nil) != (test.kind == "private" || test.kind == "private-no-inherit") {
				t.Fatal("strict private policy changed", test.kind, privateErr)
			}
		})
	}
}

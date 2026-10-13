package runtime

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeAccountRootSupplementState struct {
	descriptor, owner, aceBytes             string
	control                                 windows.SECURITY_DESCRIPTOR_CONTROL
	attributes, volume, indexHigh, indexLow uint32
	aceCount                                uint16
	nullDACL                                bool
}

func nativeAccountRootSupplementStateAt(t *testing.T, handle windows.Handle, path string) nativeAccountRootSupplementState {
	t.Helper()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	var current windows.ByHandleFileInformation
	infoErr := windows.GetFileInformationByHandle(lookup, &current)
	if err := errors.Join(infoErr, windows.CloseHandle(lookup)); err != nil {
		t.Fatal(err)
	}
	if current.VolumeSerialNumber != info.VolumeSerialNumber || current.FileIndexHigh != info.FileIndexHigh || current.FileIndexLow != info.FileIndexLow || current.FileAttributes != info.FileAttributes {
		t.Fatal("supplement root path no longer names held object")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, ownerErr := sd.Owner()
	control, _, controlErr := sd.Control()
	acl, _, aclErr := sd.DACL()
	if ownerErr != nil || owner == nil || controlErr != nil || aclErr != nil || sd.String() == "" {
		t.Fatal("actual supplement security metadata", ownerErr, controlErr, aclErr)
	}
	state := nativeAccountRootSupplementState{descriptor: sd.String(), owner: owner.String(), control: control, attributes: info.FileAttributes, volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow, nullDACL: acl == nil}
	if acl != nil {
		state.aceCount = acl.AceCount
		for i := uint32(0); i < uint32(acl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, i, &ace); err != nil {
				t.Fatal(err)
			}
			if ace.Header.AceSize == 0 {
				t.Fatal("actual supplement empty ACE")
			}
			state.aceBytes += fmt.Sprintf("%x", unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize)))
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			t.Logf("actual account root supplement ACE=%d type=%d SID=%s flags=%#x mask=%#x", i, ace.Header.AceType, sid.String(), ace.Header.AceFlags, ace.Mask)
		}
	}
	t.Logf("actual account root supplement path=%s owner=%s control=%#x null=%t ACEcount=%d attrs=%#x fileID=%d:%d:%d descriptor=%s", path, state.owner, state.control, state.nullDACL, state.aceCount, state.attributes, state.volume, state.indexHigh, state.indexLow, state.descriptor)
	return state
}

func TestNativeAccountRootACLInheritanceSupplement(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind, flags string
	}{
		{"foreign-read-object-inherit", "OI"}, {"foreign-read-container-inherit", "CI"}, {"foreign-read-both-inherit", "OICI"},
		{"null-dacl", ""}, {"empty-dacl", ""}, {"other-sid-read-only", ""},
		{"world-read-plus-write", ""}, {"other-sid-read-plus-write", ""}, {"callback-allow", ""}, {"inherit-only", "OICIIO"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "owned supplement")
			if err := EnsurePrivateDirectory(root); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "target")
			if err := EnsurePrivateDirectory(path); err != nil {
				t.Fatal(err)
			}
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := windows.CloseHandle(handle); err != nil {
					t.Error(err)
				}
			})
			originalSD, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			originalACL, _, err := originalSD.DACL()
			if err != nil || originalACL == nil {
				t.Fatal("original supplement private DACL", err)
			}
			original := nativeAccountRootSupplementStateAt(t, handle, path)
			t.Cleanup(func() {
				if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil); err != nil {
					t.Fatal("restore owned supplement ACL", err)
				}
				restored := nativeAccountRootSupplementStateAt(t, handle, path)
				if restored.owner != original.owner || restored.aceBytes != original.aceBytes || restored.aceCount != original.aceCount || restored.nullDACL || restored.control&windows.SE_DACL_PROTECTED == 0 || restored.attributes != original.attributes || restored.volume != original.volume || restored.indexHigh != original.indexHigh || restored.indexLow != original.indexLow {
					t.Fatal("owned supplement ACL restoration changed permissions or object")
				}
			})
			foreignSID := "WD"
			mask := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)
			if strings.HasPrefix(test.kind, "other-sid-") {
				foreignSID = users.String()
			}
			if strings.Contains(test.kind, "plus-write") {
				mask |= windows.FILE_WRITE_DATA
			}
			descriptor := fmt.Sprintf("D:P(A;;FA;;;%s)(A;%s;%#x;;;%s)", user.User.Sid.String(), test.flags, mask, foreignSID)
			switch test.kind {
			case "empty-dacl":
				descriptor = "D:P"
			case "callback-allow":
				descriptor = "D:P(A;;FA;;;" + user.User.Sid.String() + ")(XA;;FRFX;;;WD;(@User.Title==\"BatutaFixture\"))"
			}
			var acl *windows.ACL
			var constructionSD *windows.SECURITY_DESCRIPTOR
			if test.kind != "null-dacl" {
				constructionSD, err = windows.SecurityDescriptorFromString(descriptor)
				if err != nil {
					t.Fatal("native supplement ACL construction", test.kind, err)
				}
				acl, _, err = constructionSD.DACL()
				if err != nil || acl == nil {
					t.Fatal("native supplement constructed non-null DACL", err)
				}
			}
			if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal("set owned supplement leaf ACL", err)
			}
			before := nativeAccountRootSupplementStateAt(t, handle, path)
			if before.owner != user.User.Sid.String() || before.control&windows.SE_DACL_PROTECTED == 0 || before.attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || before.attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				t.Fatal("actual supplement owner/protection/type")
			}
			switch test.kind {
			case "null-dacl":
				if !before.nullDACL {
					t.Fatal("actual null DACL fixture is not null")
				}
			case "empty-dacl":
				if before.nullDACL || before.aceCount != 0 {
					t.Fatal("actual empty DACL fixture is not empty")
				}
			default:
				if before.nullDACL || before.aceCount != 2 {
					t.Fatal("actual supplement two-ACE fixture")
				}
				sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
				if err != nil {
					t.Fatal(err)
				}
				actualACL, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				var ace *windows.ACCESS_ALLOWED_ACE
				if err := windows.GetAce(actualACL, 1, &ace); err != nil {
					t.Fatal(err)
				}
				sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
				if test.kind == "callback-allow" {
					if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE || !strings.Contains(before.descriptor, "(XA;") {
						t.Fatal("actual conditional callback ACE type")
					}
				} else {
					expectedFlags := uint8(0)
					if strings.Contains(test.flags, "OI") {
						expectedFlags |= windows.OBJECT_INHERIT_ACE
					}
					if strings.Contains(test.flags, "CI") {
						expectedFlags |= windows.CONTAINER_INHERIT_ACE
					}
					if strings.Contains(test.flags, "IO") {
						expectedFlags |= windows.INHERIT_ONLY_ACE
					}
					if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != expectedFlags || uint32(ace.Mask) != mask || foreignSID == "WD" && !sid.IsWellKnown(windows.WinWorldSid) || foreignSID != "WD" && !windows.EqualSid(sid, users) {
						t.Fatal("actual supplement foreign ACE type/SID/flags/mask")
					}
				}
			}
			want := test.kind == "other-sid-read-only"
			f, openErr := OpenAccountRootDirectory(path)
			if f != nil {
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if (openErr == nil && f != nil) != want || openErr != nil && f != nil {
				t.Errorf("supplement Open kind=%s want=%t err=%v", test.kind, want, openErr)
			}
			if after := nativeAccountRootSupplementStateAt(t, handle, path); after != before {
				t.Fatal("supplement Open changed fault-state metadata")
			}
			ensureErr := EnsureAccountRootDirectory(path)
			if (ensureErr == nil) != want {
				t.Errorf("supplement Ensure kind=%s want=%t err=%v", test.kind, want, ensureErr)
			}
			if after := nativeAccountRootSupplementStateAt(t, handle, path); after != before {
				t.Fatal("supplement Ensure changed fault-state metadata")
			}
		})
	}
}

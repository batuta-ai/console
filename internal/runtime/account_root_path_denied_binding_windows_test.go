package runtime

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNativeAccountRootPathMetadataDeniedReplacement(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		name := "same-attributes"
		if hidden {
			name = "different-attributes"
		}
		t.Run(name, func(t *testing.T) {
			path, held, expected := nativePathFixture(t)
			if err := os.Rename(path, path+" moved"); err != nil {
				t.Fatal(err)
			}
			if err := EnsurePrivateDirectory(path); err != nil {
				t.Fatal(err)
			}
			leaf, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := windows.CreateFile(leaf, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := windows.CloseHandle(replacement); err != nil {
					t.Error(err)
				}
			})
			var actual windows.ByHandleFileInformation
			if err := windows.GetFileInformationByHandle(replacement, &actual); err != nil {
				t.Fatal(err)
			}
			if actual.VolumeSerialNumber != expected.VolumeSerialNumber || actual.FileIndexHigh == expected.FileIndexHigh && actual.FileIndexLow == expected.FileIndexLow || actual.FileAttributes != expected.FileAttributes {
				t.Fatal("distinct replacement with matching initial attributes not established")
			}
			if hidden {
				if err := windows.SetFileAttributes(leaf, actual.FileAttributes|windows.FILE_ATTRIBUTE_HIDDEN); err != nil {
					t.Fatal(err)
				}
				if err := windows.GetFileInformationByHandle(replacement, &actual); err != nil || actual.FileAttributes != expected.FileAttributes|windows.FILE_ATTRIBUTE_HIDDEN {
					t.Fatal("actual hidden replacement attributes not established", actual.FileAttributes, err)
				}
			}
			sd, err := windows.GetSecurityInfo(replacement, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			originalACL, _, err := sd.DACL()
			if err != nil || originalACL == nil {
				t.Fatal("replacement private ACL", err)
			}
			t.Cleanup(func() {
				if err := windows.SetSecurityInfo(replacement, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil); err != nil {
					t.Error("restore replacement fixture ACL", err)
				}
			})
			empty, err := windows.SecurityDescriptorFromString("D:P")
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := empty.DACL()
			if err != nil || acl == nil {
				t.Fatal("empty replacement DACL", err)
			}
			if err := windows.SetSecurityInfo(replacement, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			nativePathWithoutBackupRestore(t, func() {
				fresh, openErr := windows.CreateFile(leaf, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
				if openErr == nil {
					if err := windows.CloseHandle(fresh); err != nil {
						t.Error(err)
					}
				}
				if !errors.Is(openErr, windows.ERROR_ACCESS_DENIED) {
					t.Fatal("replacement lookup must take denied branch", openErr)
				}
				if err := nativeAccountRootPathMetadata(held, path, expected); !errors.Is(err, errNativeAccountRootPathBinding) {
					t.Fatal("denied replacement accepted as held original", err)
				}
				t.Logf("denied replacement rejected: hidden=%t originalID=%d:%d:%d replacementID=%d:%d:%d", hidden, expected.VolumeSerialNumber, expected.FileIndexHigh, expected.FileIndexLow, actual.VolumeSerialNumber, actual.FileIndexHigh, actual.FileIndexLow)
			})
		})
	}
}

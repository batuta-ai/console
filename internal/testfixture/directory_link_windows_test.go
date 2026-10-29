package testfixture

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func checkNativeDirectoryLinkIdentity(t *testing.T, create func(string, string) error) uint32 {
	t.Helper()
	root := PrivateHome(t)
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "alias")
	if err := MakePrivateDirectory(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := create(target, link); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Error(err)
		}
	}()
	var info struct{ Attributes, Tag uint32 }
	if err := windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	if info.Attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 || info.Attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || (info.Tag != windows.IO_REPARSE_TAG_MOUNT_POINT && info.Tag != windows.IO_REPARSE_TAG_SYMLINK) {
		t.Fatalf("not an actual supported directory reparse point: %+v", info)
	}
	t.Logf("actual directory reparse tag=%#x attributes=%#x", info.Tag, info.Attributes)
	return info.Tag
}

func TestDirectoryLinkNativeReparseIdentity(t *testing.T) {
	checkNativeDirectoryLinkIdentity(t, DirectoryLink)
}
func TestDirectoryJunctionNativeReparseIdentity(t *testing.T) {
	if tag := checkNativeDirectoryLinkIdentity(t, directoryJunction); tag != windows.IO_REPARSE_TAG_MOUNT_POINT {
		t.Fatalf("direct junction produced tag %#x", tag)
	}
}
func TestDirectoryJunctionReadsActualTarget(t *testing.T) {
	checkDirectoryLinkReadsActualTarget(t, directoryJunction)
}
func TestDirectoryJunctionParentRemovalPreservesTarget(t *testing.T) {
	checkDirectoryLinkParentRemovalPreservesTarget(t, directoryJunction)
}
func TestDirectoryJunctionRejectsExistingDestination(t *testing.T) {
	checkDirectoryLinkRejectsExistingDestination(t, directoryJunction)
}

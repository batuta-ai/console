package adapters_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func TestNativeReadinessDirectoryGrants(t *testing.T) {
	for _, perm := range []os.FileMode{0755, 0777} {
		t.Run(perm.String(), func(t *testing.T) {
			path := testfixture.PrivateHome(t)
			child := filepath.Join(path, "existing-child")
			if err := testfixture.PreparePrivateFile(child); err != nil {
				t.Fatal(err)
			}
			const content = "existing synthetic child bytes"
			if err := os.WriteFile(child, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			before, _ := readinessSecurityAt(t, path)
			childBefore, _ := readinessSecurityAt(t, child)
			privileges := readinessPrivileges(t)
			unchanged := prepareReadinessPermissions(t, path, perm)
			after, _ := readinessSecurityAt(t, path)
			if before.owner != after.owner || before.attributes != after.attributes || before.volume != after.volume || before.indexHigh != after.indexHigh || before.indexLow != after.indexLow || before.links != after.links {
				t.Fatalf("directory ownership/type/identity changed: before=%+v after=%+v", before, after)
			}
			if childAfter, _ := readinessSecurityAt(t, child); childBefore != childAfter {
				t.Fatalf("child security or identity changed: before=%+v after=%+v", childBefore, childAfter)
			}
			data, err := os.ReadFile(child)
			if err != nil || string(data) != content {
				t.Fatal("child bytes changed", err)
			}
			if !bytes.Equal(privileges, readinessPrivileges(t)) {
				t.Fatal("process privileges changed")
			}
			if perm == 0755 {
				f, err := consolert.OpenAccountRootDirectory(path)
				if err != nil {
					t.Fatal("Console-readable grant rejected", err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if f, err := consolert.OpenPrivateDirectory(path); err == nil {
				f.Close()
				t.Fatal("World grant accepted as private")
			}
			unchanged()
		})
	}
}

func TestNativeReadinessDirectoryGrantRefusesUnsafeObjects(t *testing.T) {
	for _, kind := range []string{"broad-dacl", "unprotected-dacl", "foreign-owner", "leaf-reparse", "parent-reparse", "file", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			target := filepath.Join(root, "target")
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sid := user.User.Sid.String()
			sddl := "O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")"
			switch kind {
			case "broad-dacl":
				sddl += "(A;;FR;;;WD)"
			case "unprotected-dacl":
				sddl = "O:" + sid + "D:(A;OICI;FA;;;" + sid + ")"
			case "foreign-owner":
				sddl = "O:BAD:P(A;OICI;FA;;;" + sid + ")"
			}
			sd, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
			attributes.Length = uint32(unsafe.Sizeof(attributes))
			name, err := windows.UTF16PtrFromString(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.CreateDirectory(name, &attributes); err != nil {
				t.Fatal("native refusal fixture creation (foreign owner requires an owner-capable token)", err)
			}
			child := filepath.Join(target, "existing-child")
			const content = "refused synthetic child bytes"
			if err := os.WriteFile(child, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			path := target
			switch kind {
			case "leaf-reparse", "parent-reparse":
				path = filepath.Join(root, "alias")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal("native reparse fixture requires symlink creation support", err)
				}
				if kind == "parent-reparse" {
					if err := consolert.EnsurePrivateDirectory(filepath.Join(target, "nested")); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(path, "nested")
				}
			case "file":
				path = child
			case "missing":
				path = filepath.Join(target, "missing")
			}
			before, beforeSD := readinessSecurityAt(t, target)
			if kind == "foreign-owner" && before.owner != "S-1-5-32-544" {
				t.Fatal("foreign owner not established", before.owner)
			}
			if kind == "unprotected-dacl" && before.control&windows.SE_DACL_PROTECTED != 0 {
				t.Fatal("unprotected DACL not established", beforeSD.String())
			}
			if kind == "broad-dacl" {
				acl, _, err := beforeSD.DACL()
				if err != nil || acl == nil || acl.AceCount != 2 {
					t.Fatal("broad DACL not established", err)
				}
			}
			childBefore, _ := readinessSecurityAt(t, child)
			observed := path
			if kind == "missing" {
				observed = target
			} else if kind == "parent-reparse" {
				observed = filepath.Join(target, "nested")
			}
			objectBefore, _ := readinessSecurityAt(t, observed)
			var reparseBefore readinessDirectorySecurity
			if kind == "leaf-reparse" || kind == "parent-reparse" {
				reparseBefore, _ = readinessSecurityAt(t, filepath.Join(root, "alias"))
				if reparseBefore.attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
					t.Fatal("native reparse point not established")
				}
			}
			privileges := readinessPrivileges(t)
			for _, perm := range []os.FileMode{0755, 0777} {
				if err := makeReadinessDirectoryGrant(path, perm); err == nil {
					t.Fatal("unsafe fixture accepted", kind, perm)
				}
			}
			if after, _ := readinessSecurityAt(t, target); before != after {
				t.Fatalf("unsafe target repaired: before=%+v after=%+v", before, after)
			}
			if after, _ := readinessSecurityAt(t, observed); objectBefore != after {
				t.Fatalf("refused object changed: before=%+v after=%+v", objectBefore, after)
			}
			if after, _ := readinessSecurityAt(t, child); childBefore != after {
				t.Fatalf("refused child changed: before=%+v after=%+v", childBefore, after)
			}
			if kind == "leaf-reparse" || kind == "parent-reparse" {
				if after, _ := readinessSecurityAt(t, filepath.Join(root, "alias")); reparseBefore != after {
					t.Fatalf("refused reparse point changed: before=%+v after=%+v", reparseBefore, after)
				}
			}
			data, err := os.ReadFile(child)
			if err != nil || string(data) != content {
				t.Fatal("refused child bytes changed", err)
			}
			if kind == "missing" {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing path created", err)
				}
			}
			if !bytes.Equal(privileges, readinessPrivileges(t)) {
				t.Fatal("refusal changed process privileges")
			}
		})
	}
}

func readinessPrivileges(t *testing.T) []byte {
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

type readinessDirectorySecurity struct {
	descriptor, owner                       string
	control                                 windows.SECURITY_DESCRIPTOR_CONTROL
	attributes, volume, indexHigh, indexLow uint32
	links                                   uint32
}

func readinessSecurityHandle(handle windows.Handle) (readinessDirectorySecurity, *windows.SECURITY_DESCRIPTOR, error) {
	var state readinessDirectorySecurity
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return state, nil, err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return state, nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return state, nil, errors.Join(err, os.ErrPermission)
	}
	control, _, err := sd.Control()
	if err != nil {
		return state, nil, err
	}
	state = readinessDirectorySecurity{sd.String(), owner.String(), control, info.FileAttributes, info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, info.NumberOfLinks}
	if state.descriptor == "" {
		return state, nil, os.ErrInvalid
	}
	return state, sd, nil
}

func readinessOpenHandle(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, access|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func readinessSecurityAt(t *testing.T, path string) (readinessDirectorySecurity, *windows.SECURITY_DESCRIPTOR) {
	t.Helper()
	handle, err := readinessOpenHandle(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	state, sd, readErr := readinessSecurityHandle(handle)
	if err := errors.Join(readErr, windows.CloseHandle(handle)); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual readiness security path=%s owner=%s control=%#x attrs=%#x fileID=%d:%d:%d links=%d descriptor=%s", path, state.owner, state.control, state.attributes, state.volume, state.indexHigh, state.indexLow, state.links, state.descriptor)
	return state, sd
}

func makeReadinessDirectoryGrant(path string, perm os.FileMode) (err error) {
	world := "FRFX"
	if perm == 0777 {
		world = "FRFWFX"
	} else if perm != 0755 {
		return os.ErrInvalid
	}
	private, err := consolert.OpenPrivateDirectory(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, private.Close()) }()
	before, originalSD, err := readinessSecurityHandle(windows.Handle(private.Fd()))
	if err != nil {
		return err
	}
	// Keep the validated, no-reparse object pinned until the DACL-only write ends.
	// The writable open must identify that same object with identical security.
	handle, err := readinessOpenHandle(path, windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(handle)) }()
	actual, _, err := readinessSecurityHandle(handle)
	if err != nil {
		return err
	}
	if actual != before || actual.attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || actual.attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return os.ErrPermission
	}
	sd, err := windows.SecurityDescriptorFromString(originalSD.String() + "(A;;" + world + ";;;WD)")
	if err != nil {
		return err
	}
	// SetSecurityInfo propagates inherited ACE removals to existing children.
	// NtSetSecurityObject applies this DACL only to the held directory object.
	setSecurity := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSetSecurityObject")
	if err := setSecurity.Find(); err != nil {
		return err
	}
	status, _, _ := setSecurity.Call(uintptr(handle), uintptr(windows.DACL_SECURITY_INFORMATION), uintptr(unsafe.Pointer(sd)))
	runtime.KeepAlive(sd)
	if windows.NTStatus(status) != 0 {
		return windows.NTStatus(status)
	}
	return nil
}

func prepareReadinessPermissions(t *testing.T, path string, perm os.FileMode) func() {
	t.Helper()
	_, originalSD := readinessSecurityAt(t, path)
	originalACL, _, err := originalSD.DACL()
	if err != nil || originalACL == nil || originalACL.AceCount != 1 {
		t.Fatal("original private directory DACL differs", err)
	}
	var originalACE *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(originalACL, 0, &originalACE); err != nil {
		t.Fatal(err)
	}
	originalFlags, originalMask := originalACE.Header.AceFlags, originalACE.Mask
	if err := makeReadinessDirectoryGrant(path, perm); err != nil {
		t.Fatal(err)
	}
	before, sd := readinessSecurityAt(t, path)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 || before.owner != user.User.Sid.String() || before.control&windows.SE_DACL_PROTECTED == 0 || before.attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || before.attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Fatal("actual readiness directory owner/DACL/type differs", before, err)
	}
	worldMask := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)
	if perm == 0777 {
		worldMask |= windows.FILE_GENERIC_WRITE
	}
	for i := uint32(0); i < 2; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			i == 0 && (!windows.EqualSid(sid, user.User.Sid) || ace.Header.AceFlags != originalFlags || ace.Mask != originalMask) ||
			i == 1 && (!sid.IsWellKnown(windows.WinWorldSid) || ace.Header.AceFlags != 0 || uint32(ace.Mask) != worldMask) {
			t.Fatal("actual readiness directory ACE SID/type/flags/mask differs", i, sid.String(), ace.Header, ace.Mask)
		}
		t.Logf("actual readiness directory ACE=%d SID=%s type=%d flags=%#x mask=%#x", i, sid.String(), ace.Header.AceType, ace.Header.AceFlags, ace.Mask)
	}
	return func() {
		t.Helper()
		after, _ := readinessSecurityAt(t, path)
		if after != before {
			t.Fatalf("readiness changed existing security: before=%+v after=%+v", before, after)
		}
	}
}

func TestNativeReadinessDirectoryGrantPreservesInheritedChild(t *testing.T) {
	for _, perm := range []os.FileMode{0755, 0777} {
		t.Run(perm.String(), func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			path := filepath.Join(root, "inheritable-parent")
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
			if err != nil {
				t.Fatal(err)
			}
			attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
			attributes.Length = uint32(unsafe.Sizeof(attributes))
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.CreateDirectory(name, &attributes); err != nil {
				t.Fatal(err)
			}
			parent, parentSD := readinessSecurityAt(t, path)
			acl, _, err := parentSD.DACL()
			if err != nil || acl == nil || acl.AceCount != 1 || parent.owner != user.User.Sid.String() || parent.control&windows.SE_DACL_PROTECTED == 0 || parent.attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || parent.attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				t.Fatal("protected current-user parent not established", parent, err)
			}
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, 0, &ace); err != nil {
				t.Fatal(err)
			}
			const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || uint32(ace.Mask) != fullAccess || !windows.EqualSid(sid, user.User.Sid) {
				t.Fatal("inheritable current-user full-access parent ACE not established", sid.String(), ace.Header, ace.Mask)
			}
			t.Logf("actual parent ACE SID=%s type=%d flags=%#x mask=%#x", sid.String(), ace.Header.AceType, ace.Header.AceFlags, ace.Mask)

			child := filepath.Join(path, "inherited-child")
			childName, err := windows.UTF16PtrFromString(child)
			if err != nil {
				t.Fatal(err)
			}
			// Retain access to inspect the child even if propagation removes its ACE.
			handle, err := windows.CreateFile(childName, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal(err)
			}
			file := os.NewFile(uintptr(handle), child)
			t.Cleanup(func() {
				if err := file.Close(); err != nil {
					t.Error(err)
				}
			})
			const content = "inherited synthetic child bytes"
			if _, err := file.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
			before, childSD, err := readinessSecurityHandle(handle)
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err = childSD.DACL()
			if err != nil || acl == nil || acl.AceCount != 1 || before.control&windows.SE_DACL_PRESENT == 0 || before.control&windows.SE_DACL_PROTECTED != 0 || before.attributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
				t.Fatal("unprotected inherited child not established", before, err)
			}
			if err := windows.GetAce(acl, 0, &ace); err != nil {
				t.Fatal(err)
			}
			sid = (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != windows.INHERITED_ACE || uint32(ace.Mask) != fullAccess || !windows.EqualSid(sid, user.User.Sid) {
				t.Fatal("inherited current-user full-access child ACE not established", sid.String(), ace.Header, ace.Mask)
			}
			t.Logf("actual inherited child security=%+v ACE SID=%s type=%d flags=%#x mask=%#x", before, sid.String(), ace.Header.AceType, ace.Header.AceFlags, ace.Mask)
			bytesBefore := make([]byte, len(content))
			if _, err := file.ReadAt(bytesBefore, 0); err != nil || string(bytesBefore) != content {
				t.Fatal("existing inherited child bytes not established", err)
			}
			infoBefore, err := file.Stat()
			if err != nil || infoBefore.Size() != int64(len(content)) {
				t.Fatal("existing inherited child size not established", err)
			}
			privileges := readinessPrivileges(t)
			if err := makeReadinessDirectoryGrant(path, perm); err != nil {
				t.Fatal(err)
			}
			after, _, err := readinessSecurityHandle(handle)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("actual inherited child after grant security=%+v", after)
			if before != after {
				t.Errorf("inherited child security or identity changed: before=%+v after=%+v", before, after)
			}
			bytesAfter := make([]byte, len(content))
			if _, err := file.ReadAt(bytesAfter, 0); err != nil || !bytes.Equal(bytesBefore, bytesAfter) {
				t.Error("inherited child bytes changed", err)
			}
			infoAfter, err := file.Stat()
			if err != nil || infoBefore.Size() != infoAfter.Size() {
				t.Error("inherited child size changed", err)
			}
			if !bytes.Equal(privileges, readinessPrivileges(t)) {
				t.Error("process privileges changed")
			}
		})
	}
}

package runtime

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func nativePathTokenInfo(t *testing.T, token windows.Token, class uint32) []byte {
	t.Helper()
	var size uint32
	if err := windows.GetTokenInformation(token, class, nil, 0, &size); !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size == 0 {
		t.Fatal("size token information", err)
	}
	data := make([]byte, size)
	if err := windows.GetTokenInformation(token, class, &data[0], size, &size); err != nil {
		t.Fatal("read token information", err)
	}
	return data[:size]
}

func nativePathTokenID(t *testing.T, token windows.Token) []byte {
	t.Helper()
	data := nativePathTokenInfo(t, token, windows.TokenStatistics)
	// TOKEN_STATISTICS begins with the kernel TokenId LUID.
	if len(data) < int(unsafe.Sizeof(windows.LUID{})) {
		t.Fatal("short TOKEN_STATISTICS")
	}
	return data[:unsafe.Sizeof(windows.LUID{})]
}

// Same-user impersonation qualifies the denial mechanism, not an ordinary-user role.
// Callbacks run synchronously on this locked thread; t.Run/Parallel would lose it.
func nativePathWithoutBackupRestore(t *testing.T, observe func()) {
	t.Helper()
	runtime.LockOSThread()
	restored := true
	defer func() {
		// On restoration failure, Goexit retires the still-locked thread.
		if restored {
			runtime.UnlockOSThread()
		}
	}()
	closeToken := func(token windows.Token) {
		if err := token.Close(); err != nil {
			t.Error("close owned token", err)
		}
	}
	var process windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &process); err != nil {
		t.Fatal(err)
	}
	defer closeToken(process)
	var original windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE, true, &original)
	if err != nil && !errors.Is(err, windows.ERROR_NO_TOKEN) {
		t.Fatal(err)
	}
	if original != 0 {
		defer closeToken(original)
	}
	caller := original
	if caller == 0 {
		caller = process
	}
	processBefore := nativePathTokenInfo(t, process, windows.TokenPrivileges)
	callerBefore := nativePathTokenInfo(t, caller, windows.TokenPrivileges)
	callerID := nativePathTokenID(t, caller)
	defer func() {
		if !bytes.Equal(processBefore, nativePathTokenInfo(t, process, windows.TokenPrivileges)) || !bytes.Equal(callerBefore, nativePathTokenInfo(t, caller, windows.TokenPrivileges)) {
			t.Error("process or caller privilege bytes changed")
		}
	}()
	var private windows.Token
	if err := windows.DuplicateTokenEx(caller, windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &private); err != nil {
		t.Fatal(err)
	}
	defer closeToken(private)
	if bytes.Equal(callerID, nativePathTokenID(t, private)) {
		t.Fatal("token was not privately duplicated")
	}
	var luids [2]windows.LUID
	for i, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		value, err := windows.UTF16PtrFromString(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.LookupPrivilegeValue(nil, value, &luids[i]); err != nil {
			t.Fatal(err)
		}
		data := nativePathTokenInfo(t, private, windows.TokenPrivileges)
		for _, privilege := range (*windows.Tokenprivileges)(unsafe.Pointer(&data[0])).AllPrivileges() {
			if privilege.Luid == luids[i] {
				state := windows.Tokenprivileges{PrivilegeCount: 1}
				state.Privileges[0].Luid = luids[i]
				if err := windows.AdjustTokenPrivileges(private, false, &state, 0, nil, nil); err != nil {
					t.Fatal("disable private token privilege", err)
				}
			}
		}
	}
	defer func() {
		restored = false
		if err := windows.SetThreadToken(nil, original); err != nil {
			t.Fatal("restore exact original thread token", err)
		}
		var current windows.Token
		err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &current)
		if err == nil {
			defer closeToken(current)
		}
		if original == 0 {
			if !errors.Is(err, windows.ERROR_NO_TOKEN) {
				t.Fatal("original nil thread token not restored", err)
			}
		} else if err != nil || !bytes.Equal(callerID, nativePathTokenID(t, current)) {
			t.Fatal("original thread token identity not restored", err)
		}
		restored = true
	}()
	if err := windows.SetThreadToken(nil, private); err != nil {
		t.Fatal("install private token", err)
	}
	effective := windows.GetCurrentThreadEffectiveToken()
	if !bytes.Equal(nativePathTokenID(t, private), nativePathTokenID(t, effective)) {
		t.Fatal("private token is not effective")
	}
	data := nativePathTokenInfo(t, effective, windows.TokenPrivileges)
	if !bytes.Equal(data, nativePathTokenInfo(t, private, windows.TokenPrivileges)) {
		t.Fatal("effective privilege bytes differ from private token")
	}
	for _, privilege := range (*windows.Tokenprivileges)(unsafe.Pointer(&data[0])).AllPrivileges() {
		if (privilege.Luid == luids[0] || privilege.Luid == luids[1]) && privilege.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 {
			t.Fatal("effective Backup/Restore privilege enabled")
		}
	}
	t.Log("private same-user effective token: Backup/Restore disabled or absent")
	observe()
}

func nativePathFixture(t *testing.T) (string, windows.Handle, windows.ByHandleFileInformation) {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "private parent")
	if err := EnsurePrivateDirectory(parent); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "target")
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
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatal("initial private ACL", err)
	}
	// Establish the setter's auto-inheritance control bits before the snapshot,
	// so cleanup can restore the exact descriptor through the same setter.
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal("establish fixture ACL baseline", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	return path, handle, info
}

func TestNativeAccountRootPathMetadataDenied(t *testing.T) {
	for _, kind := range []string{"empty-dacl", "deny-read"} {
		t.Run(kind, func(t *testing.T) {
			path, handle, info := nativePathFixture(t)
			original := nativeAccountRootSupplementStateAt(t, handle, path)
			sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			originalACL, _, err := sd.DACL()
			if err != nil || originalACL == nil {
				t.Fatal("original private ACL", err)
			}
			t.Cleanup(func() {
				if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil); err != nil {
					t.Fatal("restore fixture after observation", err)
				}
				if got := nativeAccountRootSupplementStateAt(t, handle, path); got != original {
					t.Fatal("original metadata not restored")
				}
			})
			descriptor := "D:P"
			if kind == "deny-read" {
				descriptor += "(D;;FR;;;WD)(A;;FA;;;" + original.owner + ")"
			}
			construction, err := windows.SecurityDescriptorFromString(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := construction.DACL()
			if err != nil || acl == nil {
				t.Fatal(err)
			}
			if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			nativePathWithoutBackupRestore(t, func() {
				name, err := windows.UTF16PtrFromString(path)
				if err != nil {
					t.Fatal(err)
				}
				fresh, openErr := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
				if openErr == nil {
					if err := windows.CloseHandle(fresh); err != nil {
						t.Error(err)
					}
				}
				if !errors.Is(openErr, windows.ERROR_ACCESS_DENIED) || !errors.Is(openErr, os.ErrPermission) {
					t.Fatal("old leaf lookup must fail permission", openErr)
				}
				if err := nativeAccountRootPathMetadata(handle, path, info); err != nil {
					t.Fatal("denied leaf pathname binding", err)
				}
				before := nativeAccountRootStateAt(t, handle, path)
				supplement := nativeAccountRootSupplementStateAt(t, handle, path)
				if before.descriptor != supplement.descriptor || before.owner != original.owner || before.attributes != info.FileAttributes || before.volume != info.VolumeSerialNumber || before.indexHigh != info.FileIndexHigh || before.indexLow != info.FileIndexLow || before.control&windows.SE_DACL_PROTECTED == 0 {
					t.Fatal("held metadata differs from fixture")
				}
				wantACEs := uint16(0)
				if kind == "deny-read" {
					wantACEs = 2
					actual, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
					if err != nil {
						t.Fatal(err)
					}
					actualACL, _, err := actual.DACL()
					if err != nil || actualACL == nil {
						t.Fatal(err)
					}
					var ace *windows.ACCESS_ALLOWED_ACE
					if err := windows.GetAce(actualACL, 0, &ace); err != nil {
						t.Fatal(err)
					}
					if ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != windows.FILE_GENERIC_READ || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).IsWellKnown(windows.WinWorldSid) {
						t.Fatal("actual deny-read ACE differs")
					}
				}
				if before.aceCount != wantACEs || supplement.aceCount != wantACEs || supplement.nullDACL {
					t.Fatal("actual denied ACL differs")
				}
				if after := nativeAccountRootStateAt(t, handle, path); after != before {
					t.Fatal("observer changed denied metadata")
				}
				if after := nativeAccountRootSupplementStateAt(t, handle, path); after != supplement {
					t.Fatal("observer changed denied supplement metadata")
				}
			})
		})
	}
}

func TestNativeAccountRootPathMetadataBinding(t *testing.T) {
	for _, kind := range []string{"renamed", "replaced", "removed", "attributes"} {
		t.Run(kind, func(t *testing.T) {
			path, handle, info := nativePathFixture(t)
			if err := nativeAccountRootPathMetadata(handle, path, info); err != nil {
				t.Fatal("initial binding", err)
			}
			switch kind {
			case "renamed", "replaced":
				moved := path + " moved"
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if err := nativeAccountRootPathMetadata(handle, moved, info); err != nil {
					t.Fatal("held object at new name", err)
				}
				if kind == "replaced" {
					if err := EnsurePrivateDirectory(path); err != nil {
						t.Fatal(err)
					}
				}
			case "removed":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "attributes":
				info.FileAttributes ^= windows.FILE_ATTRIBUTE_HIDDEN
			}
			if err := nativeAccountRootPathMetadata(handle, path, info); !errors.Is(err, errNativeAccountRootPathBinding) {
				t.Fatal("changed pathname/attributes must fail binding", err)
			}
			var stillHeld windows.ByHandleFileInformation
			if err := windows.GetFileInformationByHandle(handle, &stillHeld); err != nil {
				t.Fatal("observer closed original handle", err)
			}
			if stillHeld.VolumeSerialNumber != info.VolumeSerialNumber || stillHeld.FileIndexHigh != info.FileIndexHigh || stillHeld.FileIndexLow != info.FileIndexLow {
				t.Fatal("held object identity changed")
			}
		})
	}
}

func TestNativeAccountRootPathMetadataTokenRestoration(t *testing.T) {
	nativePathWithoutBackupRestore(t, func() {
		// Nested scope also exercises restoring an existing impersonation token.
		nativePathWithoutBackupRestore(t, func() {})
	})
}

var errNativeAccountRootPathBinding = errors.New("pathname no longer names held object with exact attributes")

func nativeAccountRootPathMetadata(handle windows.Handle, path string, expected windows.ByHandleFileInformation) (err error) {
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, handle, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return err
	}
	// os.File owns only the duplicate, never the fixture's original handle.
	held := os.NewFile(uintptr(duplicate), path)
	if held == nil {
		return errors.Join(os.ErrInvalid, windows.CloseHandle(duplicate))
	}
	defer func() { err = errors.Join(err, held.Close()) }()
	var actual windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(duplicate, &actual); err != nil {
		return err
	}
	if actual.VolumeSerialNumber != expected.VolumeSerialNumber || actual.FileIndexHigh != expected.FileIndexHigh || actual.FileIndexLow != expected.FileIndexLow || actual.FileAttributes != expected.FileAttributes {
		return errNativeAccountRootPathBinding
	}
	// RemoveDirectory can leave an enumerated name pending deletion until this
	// held handle closes. Such a name is no longer a live pathname binding.
	var standard struct {
		AllocationSize, EndOfFile int64
		NumberOfLinks             uint32
		DeletePending, Directory  byte
		_                         [2]byte
	}
	if err := windows.GetFileInformationByHandleEx(duplicate, windows.FileStandardInfo, (*byte)(unsafe.Pointer(&standard)), uint32(unsafe.Sizeof(standard))); err != nil {
		return err
	}
	if standard.DeletePending != 0 {
		return errNativeAccountRootPathBinding
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	lookup, openErr := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if openErr == nil {
		var current windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(lookup, &current)
		if err := errors.Join(infoErr, windows.CloseHandle(lookup)); err != nil {
			return err
		}
		if current.VolumeSerialNumber != expected.VolumeSerialNumber || current.FileIndexHigh != expected.FileIndexHigh || current.FileIndexLow != expected.FileIndexLow || current.FileAttributes != expected.FileAttributes {
			return errNativeAccountRootPathBinding
		}
		return nil
	}
	if errors.Is(openErr, os.ErrNotExist) {
		return errors.Join(errNativeAccountRootPathBinding, openErr)
	}
	if !errors.Is(openErr, windows.ERROR_ACCESS_DENIED) {
		return openErr
	}
	// Only denied opens need cached enumeration IDs; other filesystems retain
	// the direct handle-based comparison above.
	if actual.FileIndexHigh == 0 && actual.FileIndexLow == 0 {
		return errors.New("held file ID unavailable")
	}
	heldInfo, err := held.Stat()
	if err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	var volume, flags uint32
	if err := windows.GetVolumeInformationByHandle(windows.Handle(parent.Fd()), nil, 0, &volume, nil, &flags, nil, 0); err != nil {
		return err
	}
	// Go 1.27.2 os/dir_windows.go uses FileIdBothDirectoryRestartInfo only
	// with both capabilities. Reject its path-reopening fallback before SameFile.
	const cachedIDs = windows.FILE_SUPPORTS_OBJECT_IDS | windows.FILE_SUPPORTS_OPEN_BY_FILE_ID
	if flags&cachedIDs != cachedIDs {
		return errors.New("parent filesystem lacks cached directory file IDs")
	}
	if volume != expected.VolumeSerialNumber {
		return errNativeAccountRootPathBinding
	}
	entries, err := parent.Readdir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) {
			continue
		}
		attributes, ok := entry.Sys().(*syscall.Win32FileAttributeData)
		if !ok || attributes.FileAttributes != expected.FileAttributes || !os.SameFile(heldInfo, entry) {
			return errNativeAccountRootPathBinding
		}
		return nil
	}
	return errNativeAccountRootPathBinding
}

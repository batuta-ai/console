package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateWindowsDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sid := u.User.Sid.String()
	return windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;" + flags + ";FA;;;" + sid + ")")
}

func privateWindowsHandle(handle windows.Handle) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, u.User.Sid) {
		return fmt.Errorf("private object must belong to the current Windows user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("private Windows DACL must prevent inherited access")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		return fmt.Errorf("private Windows DACL must grant only the current user")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	const allFileAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || !windows.EqualSid(sid, u.User.Sid) || uint32(ace.Mask)&allFileAccess != allFileAccess {
		return fmt.Errorf("private Windows DACL must grant current-user file access")
	}
	return nil
}

func windowsLocalComponent(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.IsLocal(name) && !strings.ContainsAny(name, "/\\<>:\"|?*\x00") && !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}

func safeWindowsParent(handle windows.Handle) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (windows.EqualSid(sid, u.User.Sid) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("runtime parent must have a Windows owner")
	}
	if !trusted(owner) {
		installer, _, _, lookupErr := windows.LookupSID("", `NT SERVICE\TrustedInstaller`)
		if lookupErr != nil || !windows.EqualSid(owner, installer) {
			return fmt.Errorf("runtime parent has a foreign Windows owner")
		}
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("runtime parent must have a restrictive Windows DACL")
	}
	const unsafeRights = windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.FILE_DELETE_CHILD | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.GENERIC_WRITE | windows.GENERIC_ALL
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("runtime parent has an unsupported Windows access rule")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if uint32(ace.Mask)&unsafeRights != 0 && !trusted(sid) && !windows.EqualSid(sid, owner) {
			return fmt.Errorf("runtime parent permits foreign replacement or mutation")
		}
	}
	return nil
}

func windowsFileAt(parent windows.Handle, name string, access, disposition uint32, directory bool, sd *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	if !windowsLocalComponent(name) {
		return 0, os.ErrInvalid
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: sd}
	oa.Length = uint32(unsafe.Sizeof(oa))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, &oa, &iosb, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, disposition, options, 0, 0)
	if err != nil {
		var status windows.NTStatus
		if errors.As(err, &status) {
			switch status {
			case windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_NOT_A_DIRECTORY, windows.STATUS_FILE_IS_A_DIRECTORY:
				return 0, os.ErrInvalid
			default:
				err = status.Errno()
			}
		}
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (!directory && (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 || info.NumberOfLinks != 1)) {
		windows.CloseHandle(handle)
		return 0, os.ErrInvalid
	}
	return handle, nil
}

func windowsDirectory(path string, create bool) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, fmt.Errorf("private Windows homes require a local drive path")
	}
	parts := strings.Split(strings.TrimPrefix(absolute[len(volume):], `\`), `\`)
	if len(parts) == 0 {
		return nil, os.ErrInvalid
	}
	name, err := windows.UTF16PtrFromString(`\\?\` + volume + `\`)
	if err != nil {
		return nil, err
	}
	parent, err := windows.CreateFile(name, windows.FILE_GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(parent)
	sd, err := privateWindowsDescriptor(true)
	if err != nil {
		return nil, err
	}
	for i, part := range parts {
		if err := safeWindowsParent(parent); err != nil {
			return nil, err
		}
		disposition := uint32(windows.FILE_OPEN)
		if create {
			disposition = windows.FILE_OPEN_IF
		}
		handle, err := windowsFileAt(parent, part, windows.FILE_GENERIC_READ, disposition, true, sd)
		if err != nil {
			return nil, &os.PathError{Op: "open private directory", Path: path, Err: err}
		}
		if i == len(parts)-1 {
			if err := privateWindowsHandle(handle); err != nil {
				windows.CloseHandle(handle)
				return nil, err
			}
			return os.NewFile(uintptr(handle), absolute), nil
		}
		defer windows.CloseHandle(handle)
		parent = handle
	}
	return nil, os.ErrInvalid
}

// OpenPrivateDirectory verifies Windows ownership and DACL without changing it.
func OpenPrivateDirectory(path string) (*os.File, error) {
	return windowsDirectory(path, false)
}

// EnsurePrivateDirectory creates private Windows paths and refuses unsafe ones.
func EnsurePrivateDirectory(path string) error {
	f, err := windowsDirectory(path, true)
	if err != nil {
		return err
	}
	return f.Close()
}

func windowsPrivateFileAt(dir *os.File, name string, access, disposition uint32) (*os.File, error) {
	sd, err := privateWindowsDescriptor(false)
	if err != nil {
		return nil, err
	}
	handle, err := windowsFileAt(windows.Handle(dir.Fd()), name, access|windows.READ_CONTROL, disposition, false, sd)
	if err != nil {
		return nil, &os.PathError{Op: "open private file", Path: filepath.Join(dir.Name(), name), Err: err}
	}
	if err := privateWindowsHandle(handle); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), filepath.Join(dir.Name(), name)), nil
}

// OpenPrivateFile opens a regular single-linked Windows file in a private home.
func OpenPrivateFile(path string) (*os.File, error) {
	dir, err := OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	f, err := windowsPrivateFileAt(dir, filepath.Base(path), windows.FILE_GENERIC_READ, windows.FILE_OPEN)
	if closeErr := dir.Close(); closeErr != nil {
		if f != nil {
			closeErr = errors.Join(closeErr, f.Close())
		}
		return nil, errors.Join(err, closeErr)
	}
	return f, err
}

func windowsLockFile(f *os.File, nonblocking bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	// Lock beyond bounded metadata so separate discovery readers remain usable.
	overlap := windows.Overlapped{Offset: 0x7fffffff}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlap)
}

package runtime

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OpenAccountRootDirectory verifies an existing Console account-container root.
func OpenAccountRootDirectory(path string) (*os.File, error) {
	return windowsDirectoryWithPolicy(path, false, accountRootWindowsHandle)
}

// EnsureAccountRootDirectory creates or verifies a Console account-container root.
func EnsureAccountRootDirectory(path string) error {
	f, err := windowsDirectoryWithPolicy(path, true, accountRootWindowsHandle)
	if err != nil {
		return err
	}
	return f.Close()
}

func accountRootWindowsHandle(handle windows.Handle) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) {
		return fmt.Errorf("account root must belong to the current Windows user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("account root DACL must prevent inherited access")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("account root must have a restrictive Windows DACL")
	}
	currentFullAccess := false
	const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	const foreignReadAccess = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			return fmt.Errorf("account root has an unsupported Windows access rule")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if windows.EqualSid(sid, user.User.Sid) {
			currentFullAccess = currentFullAccess || uint32(ace.Mask)&fullAccess == fullAccess
		} else if uint32(ace.Mask)&^uint32(foreignReadAccess) != 0 || ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != 0 {
			return fmt.Errorf("account root permits foreign mutation or unsupported access")
		}
	}
	if !currentFullAccess {
		return fmt.Errorf("account root must grant current-user full access")
	}
	return nil
}

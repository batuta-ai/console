package service

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
	"unsafe"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

// Return callbacks before opening or changing HOME so the caller can register
// restoration before applying the fault.
func credentialCleanupFault(t *testing.T, home string) (func() error, func()) {
	t.Helper()
	var held windows.Handle
	var originalSD *windows.SECURITY_DESCRIPTOR
	var originalACL *windows.ACL
	var original credentialCleanupObservation
	attempted := false
	apply := func() error {
		if held != 0 {
			return errors.New("cleanup fault already holds its owned HOME")
		}
		private, err := consoleruntime.OpenPrivateDirectory(home)
		if err != nil {
			return err
		}
		if err := private.Close(); err != nil {
			return err
		}
		wide, err := windows.UTF16PtrFromString(home)
		if err != nil {
			return err
		}
		held, err = windows.CreateFile(wide, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			held = 0
			return err
		}
		originalSD, err = windows.GetSecurityInfo(held, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		originalACL, _, err = originalSD.DACL()
		if err != nil || originalACL == nil {
			return errors.Join(err, errors.New("owned cleanup fault original DACL missing"))
		}
		original, err = credentialCleanupFaultMetadata(held)
		if err != nil {
			return err
		}
		sd, err := windows.SecurityDescriptorFromString("D:P(D;;0x10001;;;" + original.owner + ")(A;;FA;;;" + original.owner + ")")
		if err != nil {
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil {
			return errors.Join(err, errors.New("owned cleanup fault DACL missing"))
		}
		// Keep the original handle and descriptor until the registered restore
		// callback, including when SetSecurityInfo reports a failure.
		attempted = true
		err = windows.SetSecurityInfo(held, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
		runtime.KeepAlive(sd)
		return err
	}
	restore := func() {
		if held == 0 {
			return
		}
		var restoreErr error
		if attempted {
			restoreErr = windows.SetSecurityInfo(held, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil)
			runtime.KeepAlive(originalSD)
			if restoreErr == nil {
				state, err := credentialCleanupFaultMetadata(held)
				restoreErr = errors.Join(restoreErr, err)
				if err == nil && (state.owner != original.owner || state.aceBytes != original.aceBytes || state.aceCount != original.aceCount || state.control&windows.SE_DACL_PROTECTED != original.control&windows.SE_DACL_PROTECTED || state.attributes != original.attributes || state.volume != original.volume || state.indexHigh != original.indexHigh || state.indexLow != original.indexLow) {
					restoreErr = errors.Join(restoreErr, errors.New("owned cleanup fault original ACL/owner/protection/attributes/identity not restored"))
				}
				private, err := consoleruntime.OpenPrivateDirectory(home)
				restoreErr = errors.Join(restoreErr, err)
				if err == nil {
					restoreErr = errors.Join(restoreErr, private.Close())
				}
			}
		}
		closeErr := windows.CloseHandle(held)
		held = 0
		if err := errors.Join(restoreErr, closeErr); err != nil {
			// Report instead of aborting the caller's remaining HOME/manager cleanup.
			t.Error("owned cleanup fault restore/close", err)
		}
	}
	return apply, restore
}

func credentialCleanupFaultMetadata(held windows.Handle) (credentialCleanupObservation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(held, &info); err != nil {
		return credentialCleanupObservation{}, err
	}
	sd, err := windows.GetSecurityInfo(held, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return credentialCleanupObservation{}, err
	}
	owner, _, ownerErr := sd.Owner()
	control, _, controlErr := sd.Control()
	acl, _, aclErr := sd.DACL()
	user, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	if err := errors.Join(ownerErr, controlErr, aclErr, userErr); err != nil {
		return credentialCleanupObservation{}, err
	}
	if owner == nil || acl == nil || !windows.EqualSid(owner, user.User.Sid) || control&windows.SE_DACL_PROTECTED == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return credentialCleanupObservation{}, errors.New("cleanup fault does not hold an owned protected native directory")
	}
	state := credentialCleanupObservation{descriptor: sd.String(), owner: owner.String(), control: control, attributes: info.FileAttributes, volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow, aceCount: acl.AceCount}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return credentialCleanupObservation{}, err
		}
		if ace.Header.AceSize == 0 {
			return credentialCleanupObservation{}, errors.New("owned cleanup fault empty ACE")
		}
		state.aceBytes += fmt.Sprintf("%x", unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize)))
	}
	return state, nil
}

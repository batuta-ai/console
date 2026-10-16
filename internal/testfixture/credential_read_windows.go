package testfixture

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func MakeCredentialWorldReadable(path string) (err error) {
	name := filepath.Base(path)
	volume := filepath.VolumeName(path)
	if !filepath.IsAbs(path) || len(volume) != 2 || volume[1] != ':' || !filepath.IsLocal(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\<>:"|?*`+"\x00") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return os.ErrInvalid
	}
	parent, err := consolert.OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	options := uint32(windows.FILE_NON_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err := windows.NtCreateFile(&handle, windows.WRITE_DAC|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, options, 0, 0); err != nil {
		var ntStatus windows.NTStatus
		if errors.As(err, &ntStatus) {
			switch ntStatus {
			case windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_NOT_A_DIRECTORY, windows.STATUS_FILE_IS_A_DIRECTORY:
				return os.ErrInvalid
			default:
				return ntStatus.Errno()
			}
		}
		return err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(handle)) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return err
	}
	if kind != windows.FILE_TYPE_DISK || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || info.NumberOfLinks != 1 {
		return os.ErrInvalid
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !windows.EqualSid(owner, user.User.Sid) {
		return os.ErrPermission
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil || acl.AceCount != 1 {
		return os.ErrPermission
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || !windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user.User.Sid) || uint32(ace.Mask)&fullAccess != fullAccess {
		return os.ErrPermission
	}
	public, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		return err
	}
	publicACL, _, err := public.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, publicACL, nil)
}

package testfixture

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func writePrivateFixtureFile(path string, data []byte) (err error) {
	name := filepath.Base(path)
	if !filepath.IsLocal(name) || strings.ContainsAny(name, "/\\<>:\"|?*\x00") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return os.ErrInvalid
	}
	dir, err := consolert.OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")")
	if err != nil {
		return err
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	options := uint32(windows.FILE_NON_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err := windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL|windows.SYNCHRONIZE, &attributes, &status, nil, 0, windows.FILE_SHARE_READ, windows.FILE_CREATE, options, 0, 0); err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return err
	}
	verified, err := consolert.OpenPrivateFile(path)
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(verified.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err == nil {
		control, _, controlErr := sd.Control()
		err = controlErr
		if err == nil && control&windows.SE_DACL_PROTECTED == 0 {
			err = os.ErrPermission
		}
	}
	return errors.Join(err, verified.Close())
}

package testfixture

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DirectoryLink creates an actual directory alias without requiring a Windows
// symbolic-link privilege for fixtures on local NTFS volumes.
func DirectoryLink(target, link string) error {
	err := os.Symlink(target, link)
	if !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return err
	}
	if fallback := directoryJunction(target, link); fallback != nil {
		return errors.Join(err, fallback)
	}
	return nil
}

func directoryJunction(target, link string) (err error) {
	absolute := target
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(filepath.Dir(link), absolute)
	}
	absolute, err = filepath.Abs(absolute)
	if err != nil {
		return err
	}
	absolute = strings.TrimPrefix(absolute, `\\?\`)
	volume := filepath.VolumeName(absolute)
	if len(volume) != 2 || volume[1] != ':' {
		return &os.LinkError{Op: "junction", Old: target, New: link, Err: windows.ERROR_NOT_SUPPORTED}
	}
	substitute, err := windows.UTF16FromString(`\??\` + absolute)
	if err != nil {
		return err
	}
	display, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	// REPARSE_DATA_BUFFER header, mount-point offsets, then two UTF-16 names.
	size := 16 + 2*(len(substitute)+len(display))
	if size > 16*1024 {
		return &os.LinkError{Op: "junction", Old: target, New: link, Err: windows.ERROR_FILENAME_EXCED_RANGE}
	}
	data := make([]byte, size)
	binary.LittleEndian.PutUint32(data[0:4], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:6], uint16(size-8))
	binary.LittleEndian.PutUint16(data[10:12], uint16((len(substitute)-1)*2))
	binary.LittleEndian.PutUint16(data[12:14], uint16(len(substitute)*2))
	binary.LittleEndian.PutUint16(data[14:16], uint16((len(display)-1)*2))
	for i, value := range append(substitute, display...) {
		binary.LittleEndian.PutUint16(data[16+2*i:], value)
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	if err = windows.CreateDirectory(name, &attributes); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(link))
		}
	}()
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(h)) }()
	var returned uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil)
}

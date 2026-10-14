//go:build windows

package service

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validSkillLinkName(name string) bool {
	return utf8.ValidString(name) && filepath.IsLocal(name) && name != "." &&
		!strings.ContainsAny(name, `/\<>:"|?*`+"\x00") &&
		!strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}

func skillDirectoryReparseData(target string) ([]byte, error) {
	volume := filepath.VolumeName(target)
	if !utf8.ValidString(target) || len(volume) != 2 || volume[1] != ':' ||
		!filepath.IsAbs(target) || filepath.Clean(target) != target || strings.Contains(target, "/") {
		return nil, os.ErrInvalid
	}
	for _, component := range strings.Split(target[len(volume)+1:], `\`) {
		if !validSkillLinkName(component) {
			return nil, os.ErrInvalid
		}
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return nil, err
	}
	driveType := windows.GetDriveType(root)
	if driveType != windows.DRIVE_FIXED && driveType != windows.DRIVE_REMOVABLE && driveType != windows.DRIVE_RAMDISK {
		return nil, os.ErrInvalid
	}
	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return nil, err
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		return nil, err
	}
	size := 16 + 2*(len(substitute)+len(printName))
	if size > windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE {
		return nil, os.ErrInvalid
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, os.ErrInvalid
	}
	// Junctions preserve directory discovery without a symbolic-link privilege.
	data := make([]byte, size)
	binary.LittleEndian.PutUint32(data, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:], uint16(size-8))
	binary.LittleEndian.PutUint16(data[10:], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(data[12:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(data[14:], uint16(2*(len(printName)-1)))
	for i, value := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(data[16+2*i:], value)
	}
	return data, nil
}

func validateSkillDirectoryTargets(selected map[string]string) error {
	for name, target := range selected {
		if !validSkillLinkName(name) {
			return os.ErrInvalid
		}
		if _, err := skillDirectoryReparseData(target); err != nil {
			return err
		}
	}
	return nil
}

func createSkillDirectoryLink(destination *os.Root, target, name string) (err error) {
	if !validSkillLinkName(name) {
		return os.ErrInvalid
	}
	data, err := skillDirectoryReparseData(target)
	if err != nil {
		return err
	}
	parent, err := destination.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := parent.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return createWindowsSkillDirectoryLink(parent, name, data)
}

func skillLinkWindowsError(err error) error {
	var status windows.NTStatus
	if errors.As(err, &status) {
		return status.Errno()
	}
	return err
}

func createWindowsSkillDirectoryLink(parent *os.File, name string, data []byte) (err error) {
	if !validSkillLinkName(name) || len(data) == 0 {
		return os.ErrInvalid
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var result windows.IO_STATUS_BLOCK
	// Exclusive create and handle-relative writes cannot replace an existing entry.
	err = windows.NtCreateFile(&handle, windows.SYNCHRONIZE|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE,
		&attributes, &result, nil, windows.FILE_ATTRIBUTE_NORMAL, 0, windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return skillLinkWindowsError(err)
	}
	defer func() {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	var returned uint32
	err = windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil)
	if err != nil {
		// Delete only the newly created object, through its still-exclusive handle.
		remove := byte(1)
		cleanupErr := windows.NtSetInformationFile(handle, &result, &remove, 1, windows.FileDispositionInformation)
		if cleanupErr != nil {
			return errors.Join(err, skillLinkWindowsError(cleanupErr))
		}
	}
	return err
}

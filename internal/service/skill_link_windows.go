//go:build windows

package service

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	size := 20 + 2*(len(substitute)+len(printName))
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
	// Directory symlinks retain both filesystem traversal and Go discovery semantics.
	data := make([]byte, size)
	binary.LittleEndian.PutUint32(data, windows.IO_REPARSE_TAG_SYMLINK)
	binary.LittleEndian.PutUint16(data[4:], uint16(size-8))
	binary.LittleEndian.PutUint16(data[10:], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(data[12:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(data[14:], uint16(2*(len(printName)-1)))
	for i, value := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(data[20+2*i:], value)
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

func withSkillDirectoryLinkPrivilege(operation func() error) (err error) {
	// Duplicate the effective caller token; never acquire authority from a different
	// process token while the caller is already impersonating.
	runtime.LockOSThread()
	var source windows.Token
	err = windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, true, &source)
	if errors.Is(err, windows.ERROR_NO_TOKEN) {
		err = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source)
	}
	runtime.UnlockOSThread()
	if err != nil {
		return fmt.Errorf("open effective skill-link token: %w", err)
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	var private windows.Token
	if err := windows.DuplicateTokenEx(source, windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &private); err != nil {
		return fmt.Errorf("duplicate skill-link token: %w", err)
	}
	defer func() {
		if closeErr := private.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	name, err := windows.UTF16PtrFromString("SeCreateSymbolicLinkPrivilege")
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return err
	}
	privileges := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}}}
	// This API can return success while reporting ERROR_NOT_ALL_ASSIGNED.
	adjust := windows.NewLazySystemDLL("advapi32.dll").NewProc("AdjustTokenPrivileges")
	ok, _, adjustErr := adjust.Call(uintptr(private), 0, uintptr(unsafe.Pointer(&privileges)), 0, 0, 0)
	if ok == 0 || adjustErr != windows.ERROR_SUCCESS {
		return fmt.Errorf("enable private symlink privilege: %w", adjustErr)
	}
	result := make(chan error)
	go func() {
		result <- func() (err error) {
			runtime.LockOSThread()
			restored := true
			defer func() {
				// A thread whose token could not be restored dies with this goroutine.
				if restored {
					runtime.UnlockOSThread()
				}
			}()
			var previous windows.Token
			err = windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_IMPERSONATE, true, &previous)
			if err != nil && !errors.Is(err, windows.ERROR_NO_TOKEN) {
				return err
			}
			if previous != 0 {
				defer func() {
					if closeErr := previous.Close(); closeErr != nil {
						err = errors.Join(err, closeErr)
					}
				}()
			}
			if err := windows.SetThreadToken(nil, private); err != nil {
				return fmt.Errorf("activate private skill-link token: %w", err)
			}
			defer func() {
				restoreErr := windows.SetThreadToken(nil, previous)
				restored = restoreErr == nil
				if restoreErr != nil {
					err = errors.Join(err, restoreErr)
				}
			}()
			return operation()
		}()
	}()
	return <-result
}

func createWindowsSkillDirectoryLink(parent *os.File, name string, data []byte) (err error) {
	if !validSkillLinkName(name) || len(data) == 0 {
		return os.ErrInvalid
	}
	return withSkillDirectoryLinkPrivilege(func() error {
		return createWindowsSkillDirectoryLinkAt(parent, name, data)
	})
}

func createWindowsSkillDirectoryLinkAt(parent *os.File, name string, data []byte) (err error) {
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

//go:build windows

package batuta

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openSetupFile(root *os.Root, path string) (*os.File, error) {
	invalid := func() (*os.File, error) {
		return nil, &os.PathError{Op: "openat", Path: path, Err: os.ErrInvalid}
	}
	if !filepath.IsLocal(path) {
		return invalid()
	}
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "<>:\"|?*\x00") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return invalid()
		}
	}
	base, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	parent := windows.Handle(base.Fd())
	// Resolve each component from the held directory handle. Reparse points
	// never become parents, including when a workspace name is replaced.
	for i, part := range parts {
		name, err := windows.NewNTUnicodeString(part)
		if err != nil {
			return invalid()
		}
		oa := windows.OBJECT_ATTRIBUTES{
			RootDirectory: parent,
			ObjectName:    name,
			Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		}
		oa.Length = uint32(unsafe.Sizeof(oa))
		options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
		last := i == len(parts)-1
		if last {
			options |= windows.FILE_NON_DIRECTORY_FILE
		} else {
			options |= windows.FILE_DIRECTORY_FILE
		}
		var handle windows.Handle
		var status windows.IO_STATUS_BLOCK
		err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ, &oa, &status, nil, 0,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_OPEN, options, 0, 0)
		if err != nil {
			var nt windows.NTStatus
			if errors.As(err, &nt) {
				switch nt {
				case windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_NOT_A_DIRECTORY, windows.STATUS_FILE_IS_A_DIRECTORY:
					return invalid()
				default:
					err = nt.Errno()
				}
			}
			return nil, &os.PathError{Op: "openat", Path: path, Err: err}
		}
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
			windows.CloseHandle(handle)
			return nil, &os.PathError{Op: "fstat", Path: path, Err: err}
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (last && (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 || info.NumberOfLinks != 1)) {
			windows.CloseHandle(handle)
			return invalid()
		}
		if last {
			kind, err := windows.GetFileType(handle)
			if err != nil || kind != windows.FILE_TYPE_DISK {
				windows.CloseHandle(handle)
				return invalid()
			}
			return os.NewFile(uintptr(handle), path), nil
		}
		defer windows.CloseHandle(handle)
		parent = handle
	}
	return invalid()
}

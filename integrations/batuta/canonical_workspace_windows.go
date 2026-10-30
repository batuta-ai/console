package batuta

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func canonicalWorkspacePath(path string) (resolved string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	buffer := make([]uint16, 32768)
	size, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if size == 0 || size >= uint32(len(buffer)) {
		return "", os.ErrInvalid
	}
	resolved = windows.UTF16ToString(buffer[:size])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
	} else if strings.HasPrefix(resolved, `\\?\`) {
		resolved = strings.TrimPrefix(resolved, `\\?\`)
	} else {
		return "", os.ErrInvalid
	}
	if !filepath.IsAbs(resolved) {
		return "", os.ErrInvalid
	}
	return resolved, nil
}

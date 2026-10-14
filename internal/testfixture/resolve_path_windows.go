package testfixture

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// ResolvePath observes the kernel target, including junctions that Go's
// EvalSymlinks leaves unresolved with its current Windows mode classification.
func ResolvePath(path string) (resolved string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
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
		return `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`), nil
	}
	if !strings.HasPrefix(resolved, `\\?\`) {
		return "", os.ErrInvalid
	}
	return strings.TrimPrefix(resolved, `\\?\`), nil
}

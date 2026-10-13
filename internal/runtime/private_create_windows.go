package runtime

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// CreatePrivateFile exclusively creates a current-SID file and returns its original writable handle.
func CreatePrivateFile(path string) (*os.File, error) {
	dir, err := OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	file, err := windowsPrivateFileAt(dir, filepath.Base(path), windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_CREATE)
	if closeErr := dir.Close(); closeErr != nil {
		if file != nil {
			closeErr = errors.Join(closeErr, file.Close())
		}
		return nil, errors.Join(err, closeErr)
	}
	return file, err
}

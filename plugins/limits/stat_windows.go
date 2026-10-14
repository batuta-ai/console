package limits

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func statImportFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if !errors.Is(err, windows.ERROR_CANT_ACCESS_FILE) {
		return info, err
	}
	// WORKAROUND: Go 1.27.2 Stat cannot follow a file symlink to an AF_UNIX
	// reparse point. Resolve metadata only; never read through this fallback.
	resolved, resolveErr := filepath.EvalSymlinks(path)
	if resolveErr != nil {
		return nil, err
	}
	physical, statErr := os.Stat(resolved)
	if statErr != nil || physical.Mode().Type() != os.ModeSocket {
		return nil, err
	}
	return physical, nil
}

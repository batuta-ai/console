//go:build !windows

package limits

import "os"

func statImportFile(path string) (os.FileInfo, error) { return os.Stat(path) }

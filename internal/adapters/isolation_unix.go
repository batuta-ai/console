//go:build !windows

package adapters

import (
	"fmt"
	"os"
	"path/filepath"
)

func checkedDirectory(path string) (os.FileInfo, error) {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("isolation path must be a directory without symlinks: %s", path)
	}
	if !credentialOwner(info) {
		return nil, fmt.Errorf("isolation directory must belong to the current user: %s", path)
	}
	return info, nil
}

func privateDirectory(path string) error {
	info, err := checkedDirectory(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0700 {
		return os.Chmod(path, 0700)
	}
	return nil
}

func prepareIsolateParent(home string) error        { return os.MkdirAll(filepath.Dir(home), 0700) }
func accountPlatformPaths(string) map[string]string { return nil }
func accountPlatformMetadata(string) []string       { return nil }
func identityDirectoryValid(path string, root bool) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !credentialOwner(info) {
		return false
	}
	if root {
		return info.Mode().Perm()&0022 == 0
	}
	return info.Mode().Perm() == 0700
}

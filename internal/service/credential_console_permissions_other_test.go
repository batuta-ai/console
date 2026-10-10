//go:build !windows

package service

import (
	"os"
	"testing"
)

func credentialExistingConsolePermissions(_ *testing.T, path string) (func(os.FileInfo) bool, error) {
	if err := os.Chmod(path, 0755); err != nil {
		return nil, err
	}
	return func(info os.FileInfo) bool { return info.Mode().Perm() == 0755 }, nil
}

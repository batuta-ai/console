//go:build !windows

package testfixture

import "os"

func MakePrivateDirectoryAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}

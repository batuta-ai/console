//go:build !windows

package testfixture

import "os"

func MakePrivateDirectory(path string, mode os.FileMode) error {
	return os.Mkdir(path, mode)
}

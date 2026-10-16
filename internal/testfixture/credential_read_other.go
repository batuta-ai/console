//go:build !windows

package testfixture

import "os"

func MakeCredentialWorldReadable(path string) error { return os.Chmod(path, 0644) }

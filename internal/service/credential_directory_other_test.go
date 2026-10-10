//go:build !windows

package service

import "os"

func credentialPrivateDirectory(_ string, info os.FileInfo) bool {
	return info.IsDir() && info.Mode().Perm() == 0700
}

package service

import "os"

func credentialPrivateDirectory(path string, info os.FileInfo) bool {
	return info.IsDir() && info.Mode().Perm() == 0700
}

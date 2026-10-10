package service

import (
	"os"

	consolert "github.com/batuta-ai/console/internal/runtime"
)

func credentialPrivateDirectory(path string, info os.FileInfo) bool {
	if !info.IsDir() {
		return false
	}
	directory, err := consolert.OpenPrivateDirectory(path)
	if err != nil {
		return false
	}
	return directory.Close() == nil
}

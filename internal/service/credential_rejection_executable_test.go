package service

import (
	"os"
	"path/filepath"
)

func credentialRejectionExecutable(adapter string) string {
	return filepath.Join(os.Getenv("PATH"), adapter)
}

package service

import (
	"os"
	"path/filepath"

	"github.com/batuta-ai/console/internal/testfixture"
)

func credentialRejectionExecutable(adapter string) string {
	return filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName(adapter))
}

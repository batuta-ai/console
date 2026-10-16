package testfixture

import (
	"os"

	consolert "github.com/batuta-ai/console/internal/runtime"
)

func MakePrivateDirectoryAll(path string, _ os.FileMode) error {
	return consolert.EnsurePrivateDirectory(path)
}

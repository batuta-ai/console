package testfixture

import (
	"os"

	consolert "github.com/batuta-ai/console/internal/runtime"
)

func MakePrivateDirectory(path string, _ os.FileMode) error {
	return consolert.EnsurePrivateDirectory(path)
}

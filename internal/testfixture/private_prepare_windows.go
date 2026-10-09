package testfixture

import (
	"errors"
	"os"

	consolert "github.com/batuta-ai/console/internal/runtime"
)

func PreparePrivateFile(path string) error {
	file, err := consolert.OpenPrivateFile(path)
	if err == nil {
		return file.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writePrivateFixtureFile(path, nil)
}

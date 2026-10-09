package runtime

import (
	"errors"
	"os"
)

// CreatePrivateFile exclusively creates a current-SID file and returns its original writable handle.
func CreatePrivateFile(path string) (*os.File, error) {
	return nil, errors.ErrUnsupported
}

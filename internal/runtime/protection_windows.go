package runtime

import (
	"errors"
	"fmt"
	"os"
)

// Windows runtime protection is not qualified. Mode bits cannot establish
// private directory, secret publication or exclusive daemon ownership there.
func windowsProtectionError() error {
	return fmt.Errorf("runtime protection on Windows: %w", errors.ErrUnsupported)
}

func prepareRuntimeHome(string) error { return windowsProtectionError() }

func LoadOrCreateAuth(string) (string, error) {
	return "", windowsProtectionError()
}

func WriteLock(string, LockInfo) (*os.File, error) {
	return nil, windowsProtectionError()
}

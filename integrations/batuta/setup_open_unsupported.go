//go:build !darwin && !linux && !windows

package batuta

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

func openSetupFile(root *os.Root, path string) (*os.File, error) {
	return nil, fmt.Errorf("safe setup file reads on %s: %w", runtime.GOOS, errors.ErrUnsupported)
}

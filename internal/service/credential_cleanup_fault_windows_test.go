package service

import (
	"os"
	"testing"
)

func credentialCleanupFault(t *testing.T, home string) (func() error, func()) {
	return func() error { return os.Chmod(home, 0) }, func() { _ = os.Chmod(home, 0700) }
}

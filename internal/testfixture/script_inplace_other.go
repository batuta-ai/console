//go:build !windows

package testfixture

import (
	"os"
	"testing"
)

func UpdateScriptInPlace(t testing.TB, path string, body []byte, mode os.FileMode) error {
	t.Helper()
	return os.WriteFile(path, body, mode)
}

package testfixture

import (
	"os"
	"testing"
)

func WriteScript(t testing.TB, path string, body []byte, mode os.FileMode) error {
	t.Helper()
	return os.WriteFile(path, body, mode)
}

package testfixture

import (
	"os"
	"testing"
)

func RewriteScript(t testing.TB, path string, body []byte, mode os.FileMode) error {
	t.Helper()
	return os.WriteFile(path, body, mode)
}

func RemoveScript(t testing.TB, path string) error {
	t.Helper()
	return os.Remove(path)
}

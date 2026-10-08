package testfixture

import (
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/runtime"
)

func PrivateHome(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private home")
	file, err := runtime.EnsurePrivateDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

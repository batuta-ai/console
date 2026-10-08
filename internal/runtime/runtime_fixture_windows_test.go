package runtime

import (
	"os"
	"testing"
)

func runtimeTestHome(t *testing.T) string {
	t.Helper()
	return task43PrivateHome(t)
}

func assertRuntimeFileProtection(t *testing.T, path string, st os.FileInfo) {
	t.Helper()
	task43AssertPrivateACL(t, path)
}

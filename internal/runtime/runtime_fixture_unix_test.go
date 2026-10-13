//go:build !windows

package runtime

import (
	"os"
	"testing"
)

func runtimeTestHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func assertRuntimeFileProtection(t *testing.T, path string, st os.FileInfo) {
	t.Helper()
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

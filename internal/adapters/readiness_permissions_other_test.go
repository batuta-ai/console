//go:build !windows

package adapters_test

import (
	"os"
	"testing"
)

func prepareReadinessPermissions(t *testing.T, path string, perm os.FileMode) func() {
	t.Helper()
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != perm {
			t.Fatalf("readiness changed existing permissions: %v %v", info, err)
		}
	}
}

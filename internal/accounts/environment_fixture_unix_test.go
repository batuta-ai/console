//go:build !windows

package accounts

import (
	"os"
	"path/filepath"
	"testing"
)

func sessionEnvironmentTestHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "owned-session")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	return home
}

//go:build !windows

package service

import (
	"os"
	"testing"
)

func snapshotCallerFiles(t *testing.T, credentialPath, settingsPath string) func() {
	t.Helper()
	return func() {
		t.Helper()
		for _, path := range []string{credentialPath, settingsPath} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("caller file mode changed")
			}
		}
	}
}
